package application_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/vit3ro/transaction-execution-api/internal/application"
	"github.com/vit3ro/transaction-execution-api/internal/domain"
	"github.com/vit3ro/transaction-execution-api/internal/ports"
)

type executeFixture struct {
	repository *fakeRepository
	provider   *fakeProvider
	useCase    *application.ExecuteTransaction
}

func newExecuteFixture() *executeFixture {
	repository := &fakeRepository{}
	provider := &fakeProvider{}
	useCase := application.NewExecuteTransaction(
		repository,
		provider,
		fixedClock{now: fixedNow},
		fixedIDGenerator{id: fixedID},
		discardLogger(),
	)
	return &executeFixture{repository: repository, provider: provider, useCase: useCase}
}

func validInput(t *testing.T) application.ExecuteTransactionInput {
	t.Helper()
	return application.ExecuteTransactionInput{
		AccountID:   "acc-123456",
		Type:        domain.TransactionTypeCredit,
		Amount:      mustParseMoney(t, "1500.00"),
		Currency:    domain.CurrencyMXN,
		Description: "Transferencia recibida",
	}
}

func TestExecuteTransaction_Approved(t *testing.T) {
	fixture := newExecuteFixture()
	balance := mustParseMoney(t, "5500.00")
	fixture.provider.result = ports.ProviderResult{
		Status:        ports.ProviderStatusApproved,
		TransactionID: "txn-789",
		Balance:       balance,
		ExecutedAt:    fixedNow,
	}
	input := validInput(t)

	result, err := fixture.useCase.Execute(context.Background(), input)
	transaction := result.Transaction
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}

	if transaction.ID != fixedID {
		t.Errorf("ID = %s, want %s", transaction.ID, fixedID)
	}
	if transaction.Status != domain.TransactionStatusExecuted {
		t.Errorf("Status = %q, want EXECUTED", transaction.Status)
	}
	if transaction.ProviderTransactionID != "txn-789" {
		t.Errorf("ProviderTransactionID = %q, want txn-789", transaction.ProviderTransactionID)
	}
	if transaction.BalanceAfter == nil || *transaction.BalanceAfter != balance {
		t.Errorf("BalanceAfter = %v, want %s", transaction.BalanceAfter, balance)
	}
	if !transaction.CreatedAt.Equal(fixedNow) {
		t.Errorf("CreatedAt = %v, want %v", transaction.CreatedAt, fixedNow)
	}

	if len(fixture.repository.created) != 1 {
		t.Fatalf("Create calls = %d, want 1", len(fixture.repository.created))
	}
	if got := fixture.repository.created[0].Status; got != domain.TransactionStatusPending {
		t.Errorf("status persisted before calling the provider = %q, want PENDING", got)
	}
	if len(fixture.repository.updated) != 1 {
		t.Fatalf("Update calls = %d, want 1", len(fixture.repository.updated))
	}
	if got := fixture.repository.updated[0].Status; got != domain.TransactionStatusExecuted {
		t.Errorf("status persisted after the provider answered = %q, want EXECUTED", got)
	}

	wantRequest := ports.ProviderRequest{
		AccountID: input.AccountID,
		Type:      input.Type,
		Amount:    input.Amount,
		Currency:  input.Currency,
	}
	if len(fixture.provider.requests) != 1 || fixture.provider.requests[0] != wantRequest {
		t.Errorf("provider requests = %+v, want exactly %+v", fixture.provider.requests, wantRequest)
	}
}

func TestExecuteTransaction_RejectedForInsufficientFunds(t *testing.T) {
	fixture := newExecuteFixture()
	fixture.provider.result = ports.ProviderResult{
		Status:           ports.ProviderStatusRejected,
		RejectionCode:    "INSUFFICIENT_FUNDS",
		RejectionMessage: "Account balance is not enough",
	}
	input := validInput(t)
	input.Type = domain.TransactionTypeDebit

	result, err := fixture.useCase.Execute(context.Background(), input)
	transaction := result.Transaction
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}

	if transaction.Status != domain.TransactionStatusRejected {
		t.Errorf("Status = %q, want REJECTED", transaction.Status)
	}
	if transaction.RejectionCode != "INSUFFICIENT_FUNDS" {
		t.Errorf("RejectionCode = %q, want INSUFFICIENT_FUNDS", transaction.RejectionCode)
	}
	if transaction.RejectionMessage != "Account balance is not enough" {
		t.Errorf("RejectionMessage = %q, want the provider message", transaction.RejectionMessage)
	}
	if transaction.ProviderTransactionID != "" || transaction.BalanceAfter != nil {
		t.Error("a rejected transaction must not carry execution results")
	}
	if len(fixture.repository.updated) != 1 || fixture.repository.updated[0].Status != domain.TransactionStatusRejected {
		t.Errorf("persisted outcome = %+v, want one REJECTED update", fixture.repository.updated)
	}
}

func TestExecuteTransaction_ProviderFailures(t *testing.T) {
	tests := []struct {
		name     string
		result   ports.ProviderResult
		err      error
		wantCode string
	}{
		{
			name:     "provider timeout",
			err:      fmt.Errorf("execute request: %w", context.DeadlineExceeded),
			wantCode: application.FailureCodeProviderTimeout,
		},
		{
			name:     "network error",
			err:      errors.New("dial tcp: connection refused"),
			wantCode: application.FailureCodeProviderUnavailable,
		},
		{
			name:     "response outside the contract",
			err:      fmt.Errorf("%w: http status 503", ports.ErrProviderInvalidResponse),
			wantCode: application.FailureCodeProviderInvalidResponse,
		},
		{
			name:     "response with unknown status",
			result:   ports.ProviderResult{Status: "UNKNOWN"},
			wantCode: application.FailureCodeProviderInvalidResponse,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newExecuteFixture()
			fixture.provider.result = tt.result
			fixture.provider.err = tt.err

			result, err := fixture.useCase.Execute(context.Background(), validInput(t))
			transaction := result.Transaction
			if err != nil {
				t.Fatalf("Execute returned unexpected error: %v", err)
			}

			if transaction.Status != domain.TransactionStatusFailed {
				t.Errorf("Status = %q, want FAILED", transaction.Status)
			}
			if transaction.RejectionCode != tt.wantCode {
				t.Errorf("RejectionCode = %q, want %q", transaction.RejectionCode, tt.wantCode)
			}
			if transaction.RejectionMessage == "" {
				t.Error("a failed transaction must describe the failure")
			}
			if len(fixture.repository.updated) != 1 || fixture.repository.updated[0].Status != domain.TransactionStatusFailed {
				t.Errorf("persisted outcome = %+v, want one FAILED update", fixture.repository.updated)
			}
		})
	}
}

func TestExecuteTransaction_DomainErrorSkipsPersistenceAndProvider(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*application.ExecuteTransactionInput)
		wantErr error
	}{
		{
			name:    "amount at the minimum",
			mutate:  func(in *application.ExecuteTransactionInput) { in.Amount = domain.NewMoneyFromCents(100) },
			wantErr: domain.ErrAmountBelowMinimum,
		},
		{
			name: "debit over the limit",
			mutate: func(in *application.ExecuteTransactionInput) {
				in.Type = domain.TransactionTypeDebit
				in.Amount = domain.NewMoneyFromCents(1000001)
			},
			wantErr: domain.ErrDebitAmountExceedsLimit,
		},
		{
			name:    "unsupported currency",
			mutate:  func(in *application.ExecuteTransactionInput) { in.Currency = "USD" },
			wantErr: domain.ErrUnsupportedCurrency,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newExecuteFixture()
			input := validInput(t)
			tt.mutate(&input)

			result, err := fixture.useCase.Execute(context.Background(), input)
			transaction := result.Transaction

			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Execute error = %v, want %v", err, tt.wantErr)
			}
			if transaction != nil {
				t.Error("Execute must not return a transaction when a business rule fails")
			}
			if len(fixture.repository.created) != 0 || len(fixture.repository.updated) != 0 {
				t.Error("a transaction that breaks a business rule must not be persisted")
			}
			if len(fixture.provider.requests) != 0 {
				t.Error("the provider must not be called when a business rule fails")
			}
		})
	}
}

func TestExecuteTransaction_CreateFailureSkipsProvider(t *testing.T) {
	fixture := newExecuteFixture()
	storageErr := errors.New("connection pool exhausted")
	fixture.repository.createErr = storageErr

	result, err := fixture.useCase.Execute(context.Background(), validInput(t))
	transaction := result.Transaction

	if !errors.Is(err, storageErr) {
		t.Fatalf("Execute error = %v, want it to wrap %v", err, storageErr)
	}
	if transaction != nil {
		t.Error("Execute must not return a transaction that was never persisted")
	}
	if len(fixture.provider.requests) != 0 {
		t.Error("the provider must not be called when the PENDING transaction was not persisted")
	}
}

func TestExecuteTransaction_UpdateFailureIsReported(t *testing.T) {
	fixture := newExecuteFixture()
	storageErr := errors.New("connection reset")
	fixture.repository.updateErr = storageErr
	fixture.provider.result = ports.ProviderResult{
		Status:        ports.ProviderStatusApproved,
		TransactionID: "txn-789",
		Balance:       mustParseMoney(t, "5500.00"),
	}

	result, err := fixture.useCase.Execute(context.Background(), validInput(t))
	transaction := result.Transaction

	if !errors.Is(err, storageErr) {
		t.Fatalf("Execute error = %v, want it to wrap %v", err, storageErr)
	}
	if !errors.Is(err, application.ErrOutcomeNotPersisted) {
		t.Errorf("Execute error = %v, want ErrOutcomeNotPersisted", err)
	}
	if transaction != nil {
		t.Error("Execute must not return a transaction whose outcome was not persisted")
	}
	if len(fixture.provider.requests) != 1 {
		t.Errorf("provider calls = %d, want 1", len(fixture.provider.requests))
	}
	if len(fixture.repository.created) != 1 {
		t.Errorf("Create calls = %d, want the PENDING record to remain as evidence", len(fixture.repository.created))
	}
}

func TestExecuteTransaction_PersistsOutcomeEvenIfCallerCancels(t *testing.T) {
	fixture := newExecuteFixture()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fixture.provider.onExecute = cancel
	fixture.provider.result = ports.ProviderResult{
		Status:        ports.ProviderStatusApproved,
		TransactionID: "txn-789",
		Balance:       mustParseMoney(t, "5500.00"),
	}

	result, err := fixture.useCase.Execute(ctx, validInput(t))
	transaction := result.Transaction
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}

	if fixture.repository.updateCtxErr != nil {
		t.Errorf("Update received a context with error %v, want a live context", fixture.repository.updateCtxErr)
	}
	if transaction.Status != domain.TransactionStatusExecuted {
		t.Errorf("Status = %q, want EXECUTED", transaction.Status)
	}
}

func TestExecuteTransaction_IDGenerationFailure(t *testing.T) {
	repository := &fakeRepository{}
	provider := &fakeProvider{}
	idErr := errors.New("entropy source unavailable")
	useCase := application.NewExecuteTransaction(
		repository,
		provider,
		fixedClock{now: fixedNow},
		fixedIDGenerator{err: idErr},
		discardLogger(),
	)

	_, err := useCase.Execute(context.Background(), validInput(t))

	if !errors.Is(err, idErr) {
		t.Fatalf("Execute error = %v, want it to wrap %v", err, idErr)
	}
	if len(repository.created) != 0 || len(provider.requests) != 0 {
		t.Error("nothing must be persisted or executed without a transaction id")
	}
}
