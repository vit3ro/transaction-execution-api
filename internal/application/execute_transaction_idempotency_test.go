package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/vit3ro/transaction-execution-api/internal/application"
	"github.com/vit3ro/transaction-execution-api/internal/domain"
	"github.com/vit3ro/transaction-execution-api/internal/ports"
)

const idempotencyKey = "order-42"

func storedExecution(t *testing.T, input application.ExecuteTransactionInput) *domain.Transaction {
	t.Helper()
	transaction, err := domain.NewTransaction(domain.NewTransactionParams{
		ID:             fixedID,
		AccountID:      input.AccountID,
		Type:           input.Type,
		Amount:         input.Amount,
		Currency:       input.Currency,
		Description:    input.Description,
		IdempotencyKey: input.IdempotencyKey,
		CreatedAt:      fixedNow,
	})
	if err != nil {
		t.Fatalf("build stored transaction: %v", err)
	}
	if err := transaction.MarkExecuted("txn-789", mustParseMoney(t, "5500.00"), fixedNow); err != nil {
		t.Fatalf("resolve stored transaction: %v", err)
	}
	return transaction
}

func keyedInput(t *testing.T) application.ExecuteTransactionInput {
	t.Helper()
	input := validInput(t)
	input.IdempotencyKey = idempotencyKey
	return input
}

func TestExecuteTransaction_StoresIdempotencyKey(t *testing.T) {
	fixture := newExecuteFixture()
	fixture.provider.result = ports.ProviderResult{
		Status:        ports.ProviderStatusApproved,
		TransactionID: "txn-789",
		Balance:       mustParseMoney(t, "5500.00"),
	}

	result, err := fixture.useCase.Execute(context.Background(), keyedInput(t))
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}

	if result.Replayed {
		t.Error("a first execution must not be reported as replayed")
	}
	if len(fixture.repository.created) != 1 || fixture.repository.created[0].IdempotencyKey != idempotencyKey {
		t.Errorf("created = %+v, want one transaction carrying the idempotency key", fixture.repository.created)
	}
	if len(fixture.provider.requests) != 1 {
		t.Errorf("provider calls = %d, want 1", len(fixture.provider.requests))
	}
}

func TestExecuteTransaction_WithoutKeySkipsLookup(t *testing.T) {
	fixture := newExecuteFixture()
	fixture.provider.result = ports.ProviderResult{Status: ports.ProviderStatusApproved, TransactionID: "txn-789"}

	if _, err := fixture.useCase.Execute(context.Background(), validInput(t)); err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}

	if fixture.repository.findCalls != 0 {
		t.Errorf("idempotency lookups = %d, want 0 without a key", fixture.repository.findCalls)
	}
}

func TestExecuteTransaction_RepeatedKeyReturnsOriginalWithoutExecuting(t *testing.T) {
	fixture := newExecuteFixture()
	input := keyedInput(t)
	original := storedExecution(t, input)
	fixture.repository.find = func(int, string) (*domain.Transaction, error) { return original, nil }

	result, err := fixture.useCase.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}

	if !result.Replayed || result.Transaction != original {
		t.Errorf("result = %+v, want the original transaction marked as replayed", result)
	}
	if len(fixture.provider.requests) != 0 {
		t.Error("the provider must not be called again for a repeated idempotency key")
	}
	if len(fixture.repository.created) != 0 || len(fixture.repository.updated) != 0 {
		t.Error("nothing must be persisted for a repeated idempotency key")
	}
}

func TestExecuteTransaction_KeyReusedWithDifferentRequest(t *testing.T) {
	mutations := map[string]func(*application.ExecuteTransactionInput){
		"different amount":  func(in *application.ExecuteTransactionInput) { in.Amount = domain.NewMoneyFromCents(999900) },
		"different account": func(in *application.ExecuteTransactionInput) { in.AccountID = "acc-other" },
		"different type":    func(in *application.ExecuteTransactionInput) { in.Type = domain.TransactionTypeDebit },
	}

	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			fixture := newExecuteFixture()
			original := storedExecution(t, keyedInput(t))
			fixture.repository.find = func(int, string) (*domain.Transaction, error) { return original, nil }
			input := keyedInput(t)
			mutate(&input)

			result, err := fixture.useCase.Execute(context.Background(), input)

			if !errors.Is(err, application.ErrIdempotencyKeyReused) {
				t.Fatalf("Execute error = %v, want ErrIdempotencyKeyReused", err)
			}
			if result.Transaction != nil {
				t.Error("a mismatching request must not receive the original transaction")
			}
			if len(fixture.provider.requests) != 0 {
				t.Error("the provider must not be called when the key is reused")
			}
		})
	}
}

func TestExecuteTransaction_ConcurrentKeyReturnsWinner(t *testing.T) {
	fixture := newExecuteFixture()
	input := keyedInput(t)
	winner := storedExecution(t, input)
	fixture.repository.createErr = ports.ErrIdempotencyKeyConflict
	fixture.repository.find = func(call int, _ string) (*domain.Transaction, error) {
		if call == 1 {
			return nil, ports.ErrTransactionNotFound
		}
		return winner, nil
	}

	result, err := fixture.useCase.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}

	if !result.Replayed || result.Transaction != winner {
		t.Errorf("result = %+v, want the transaction that won the race marked as replayed", result)
	}
	if len(fixture.provider.requests) != 0 {
		t.Error("the request that lost the race must not call the provider")
	}
}

func TestExecuteTransaction_IdempotencyLookupFailure(t *testing.T) {
	fixture := newExecuteFixture()
	storageErr := errors.New("connection reset")
	fixture.repository.find = func(int, string) (*domain.Transaction, error) { return nil, storageErr }

	_, err := fixture.useCase.Execute(context.Background(), keyedInput(t))

	if !errors.Is(err, storageErr) {
		t.Fatalf("Execute error = %v, want it to wrap %v", err, storageErr)
	}
	if len(fixture.repository.created) != 0 || len(fixture.provider.requests) != 0 {
		t.Error("nothing must be persisted or executed when the idempotency lookup fails")
	}
}
