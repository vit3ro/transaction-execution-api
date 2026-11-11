package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vit3ro/transaction-execution-api/internal/domain"
)

var (
	testID         = uuid.MustParse("0192f0c1-7c3a-7b1e-8f00-000000000001")
	testCreatedAt  = time.Date(2025, 3, 15, 10, 30, 0, 0, time.UTC)
	testResolvedAt = testCreatedAt.Add(2 * time.Second)
)

func mustParseMoney(t *testing.T, value string) domain.Money {
	t.Helper()
	money, err := domain.ParseMoney(value)
	if err != nil {
		t.Fatalf("invalid test amount %q: %v", value, err)
	}
	return money
}

func validParams(t *testing.T) domain.NewTransactionParams {
	t.Helper()
	return domain.NewTransactionParams{
		ID:          testID,
		AccountID:   "acc-123456",
		Type:        domain.TransactionTypeCredit,
		Amount:      mustParseMoney(t, "1500.00"),
		Currency:    domain.CurrencyMXN,
		Description: "Transferencia recibida",
		CreatedAt:   testCreatedAt,
	}
}

func newPendingTransaction(t *testing.T) *domain.Transaction {
	t.Helper()
	transaction, err := domain.NewTransaction(validParams(t))
	if err != nil {
		t.Fatalf("NewTransaction returned unexpected error: %v", err)
	}
	return transaction
}

func TestNewTransaction_Success(t *testing.T) {
	params := validParams(t)

	transaction, err := domain.NewTransaction(params)
	if err != nil {
		t.Fatalf("NewTransaction returned unexpected error: %v", err)
	}

	if transaction.ID != params.ID {
		t.Errorf("ID = %s, want %s", transaction.ID, params.ID)
	}
	if transaction.AccountID != params.AccountID {
		t.Errorf("AccountID = %q, want %q", transaction.AccountID, params.AccountID)
	}
	if transaction.Type != params.Type {
		t.Errorf("Type = %q, want %q", transaction.Type, params.Type)
	}
	if transaction.Amount != params.Amount {
		t.Errorf("Amount = %s, want %s", transaction.Amount, params.Amount)
	}
	if transaction.Currency != params.Currency {
		t.Errorf("Currency = %q, want %q", transaction.Currency, params.Currency)
	}
	if transaction.Description != params.Description {
		t.Errorf("Description = %q, want %q", transaction.Description, params.Description)
	}
	if transaction.Status != domain.TransactionStatusPending {
		t.Errorf("Status = %q, want PENDING", transaction.Status)
	}
	if transaction.ProviderTransactionID != "" || transaction.BalanceAfter != nil {
		t.Error("a new transaction must not carry provider results")
	}
	if transaction.RejectionCode != "" || transaction.RejectionMessage != "" {
		t.Error("a new transaction must not carry rejection details")
	}
	if !transaction.CreatedAt.Equal(testCreatedAt) || !transaction.UpdatedAt.Equal(testCreatedAt) {
		t.Errorf("CreatedAt = %v and UpdatedAt = %v, want both %v",
			transaction.CreatedAt, transaction.UpdatedAt, testCreatedAt)
	}
}

func TestNewTransaction_NormalizesCreatedAt(t *testing.T) {
	mexicoCity := time.FixedZone("America/Mexico_City", -6*60*60)
	params := validParams(t)
	params.CreatedAt = time.Date(2025, 3, 15, 4, 30, 0, 123456789, mexicoCity)

	transaction, err := domain.NewTransaction(params)
	if err != nil {
		t.Fatalf("NewTransaction returned unexpected error: %v", err)
	}

	want := time.Date(2025, 3, 15, 10, 30, 0, 123456000, time.UTC)
	if transaction.CreatedAt != want {
		t.Errorf("CreatedAt = %v, want %v (UTC, microsecond precision)", transaction.CreatedAt, want)
	}
}

func TestNewTransaction_AmountRules(t *testing.T) {
	tests := []struct {
		name    string
		txType  domain.TransactionType
		amount  string
		wantErr error
	}{
		{"debit at 1.00 is below minimum", domain.TransactionTypeDebit, "1.00", domain.ErrAmountBelowMinimum},
		{"credit at 1.00 is below minimum", domain.TransactionTypeCredit, "1.00", domain.ErrAmountBelowMinimum},
		{"debit at 0.99 is below minimum", domain.TransactionTypeDebit, "0.99", domain.ErrAmountBelowMinimum},
		{"zero amount is below minimum", domain.TransactionTypeCredit, "0", domain.ErrAmountBelowMinimum},
		{"negative amount is below minimum", domain.TransactionTypeCredit, "-50.00", domain.ErrAmountBelowMinimum},
		{"debit at 1.01 is accepted", domain.TransactionTypeDebit, "1.01", nil},
		{"credit at 1.01 is accepted", domain.TransactionTypeCredit, "1.01", nil},
		{"debit at 10000.00 is accepted", domain.TransactionTypeDebit, "10000.00", nil},
		{"debit at 10000.01 exceeds limit", domain.TransactionTypeDebit, "10000.01", domain.ErrDebitAmountExceedsLimit},
		{"credit at 10000.01 is accepted", domain.TransactionTypeCredit, "10000.01", nil},
		{"credit with very high amount is accepted", domain.TransactionTypeCredit, "999999999999.99", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := validParams(t)
			params.Type = tt.txType
			params.Amount = mustParseMoney(t, tt.amount)

			transaction, err := domain.NewTransaction(params)

			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("NewTransaction returned unexpected error: %v", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("NewTransaction error = %v, want %v", err, tt.wantErr)
			}
			if transaction != nil {
				t.Error("NewTransaction must not return a transaction when a rule fails")
			}
			assertValidationField(t, err, "amount")
		})
	}
}

func TestNewTransaction_InputValidation(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*domain.NewTransactionParams)
		wantErr   error
		wantField string
	}{
		{
			name:      "empty account id",
			mutate:    func(p *domain.NewTransactionParams) { p.AccountID = "" },
			wantErr:   domain.ErrAccountIDRequired,
			wantField: "accountId",
		},
		{
			name:      "blank account id",
			mutate:    func(p *domain.NewTransactionParams) { p.AccountID = "   " },
			wantErr:   domain.ErrAccountIDRequired,
			wantField: "accountId",
		},
		{
			name:      "unknown transaction type",
			mutate:    func(p *domain.NewTransactionParams) { p.Type = "TRANSFER" },
			wantErr:   domain.ErrInvalidTransactionType,
			wantField: "type",
		},
		{
			name:      "empty transaction type",
			mutate:    func(p *domain.NewTransactionParams) { p.Type = "" },
			wantErr:   domain.ErrInvalidTransactionType,
			wantField: "type",
		},
		{
			name:      "lowercase transaction type",
			mutate:    func(p *domain.NewTransactionParams) { p.Type = "credit" },
			wantErr:   domain.ErrInvalidTransactionType,
			wantField: "type",
		},
		{
			name:      "USD currency",
			mutate:    func(p *domain.NewTransactionParams) { p.Currency = "USD" },
			wantErr:   domain.ErrUnsupportedCurrency,
			wantField: "currency",
		},
		{
			name:      "empty currency",
			mutate:    func(p *domain.NewTransactionParams) { p.Currency = "" },
			wantErr:   domain.ErrUnsupportedCurrency,
			wantField: "currency",
		},
		{
			name:      "lowercase currency",
			mutate:    func(p *domain.NewTransactionParams) { p.Currency = "mxn" },
			wantErr:   domain.ErrUnsupportedCurrency,
			wantField: "currency",
		},
		{
			name: "description over the maximum length",
			mutate: func(p *domain.NewTransactionParams) {
				p.Description = strings.Repeat("a", domain.MaxDescriptionLength+1)
			},
			wantErr:   domain.ErrDescriptionTooLong,
			wantField: "description",
		},
		{
			name: "idempotency key over the maximum length",
			mutate: func(p *domain.NewTransactionParams) {
				p.IdempotencyKey = strings.Repeat("k", domain.MaxIdempotencyKeyLength+1)
			},
			wantErr:   domain.ErrIdempotencyKeyTooLong,
			wantField: "idempotencyKey",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := validParams(t)
			tt.mutate(&params)

			transaction, err := domain.NewTransaction(params)

			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("NewTransaction error = %v, want %v", err, tt.wantErr)
			}
			if transaction != nil {
				t.Error("NewTransaction must not return a transaction when validation fails")
			}
			assertValidationField(t, err, tt.wantField)
		})
	}
}

func TestNewTransaction_DescriptionBoundaries(t *testing.T) {
	descriptions := map[string]string{
		"empty description":             "",
		"description at maximum length": strings.Repeat("a", domain.MaxDescriptionLength),
		"multibyte at maximum length":   strings.Repeat("ñ", domain.MaxDescriptionLength),
	}

	for name, description := range descriptions {
		t.Run(name, func(t *testing.T) {
			params := validParams(t)
			params.Description = description

			if _, err := domain.NewTransaction(params); err != nil {
				t.Errorf("NewTransaction returned unexpected error: %v", err)
			}
		})
	}
}

func TestTransaction_MarkExecuted(t *testing.T) {
	transaction := newPendingTransaction(t)
	balance := mustParseMoney(t, "5500.00")

	if err := transaction.MarkExecuted("txn-789", balance, testResolvedAt); err != nil {
		t.Fatalf("MarkExecuted returned unexpected error: %v", err)
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
	if !transaction.UpdatedAt.Equal(testResolvedAt) {
		t.Errorf("UpdatedAt = %v, want %v", transaction.UpdatedAt, testResolvedAt)
	}
	if !transaction.CreatedAt.Equal(testCreatedAt) {
		t.Errorf("CreatedAt = %v, want it unchanged at %v", transaction.CreatedAt, testCreatedAt)
	}
}

func TestTransaction_MarkRejected(t *testing.T) {
	transaction := newPendingTransaction(t)

	if err := transaction.MarkRejected("INSUFFICIENT_FUNDS", "Not enough balance", testResolvedAt); err != nil {
		t.Fatalf("MarkRejected returned unexpected error: %v", err)
	}

	if transaction.Status != domain.TransactionStatusRejected {
		t.Errorf("Status = %q, want REJECTED", transaction.Status)
	}
	if transaction.RejectionCode != "INSUFFICIENT_FUNDS" || transaction.RejectionMessage != "Not enough balance" {
		t.Errorf("rejection details = (%q, %q), want (INSUFFICIENT_FUNDS, Not enough balance)",
			transaction.RejectionCode, transaction.RejectionMessage)
	}
	if transaction.ProviderTransactionID != "" || transaction.BalanceAfter != nil {
		t.Error("a rejected transaction must not carry execution results")
	}
}

func TestTransaction_MarkFailed(t *testing.T) {
	transaction := newPendingTransaction(t)

	if err := transaction.MarkFailed("PROVIDER_UNAVAILABLE", "provider request timed out", testResolvedAt); err != nil {
		t.Fatalf("MarkFailed returned unexpected error: %v", err)
	}

	if transaction.Status != domain.TransactionStatusFailed {
		t.Errorf("Status = %q, want FAILED", transaction.Status)
	}
	if transaction.RejectionCode != "PROVIDER_UNAVAILABLE" || transaction.RejectionMessage != "provider request timed out" {
		t.Errorf("failure details = (%q, %q), want (PROVIDER_UNAVAILABLE, provider request timed out)",
			transaction.RejectionCode, transaction.RejectionMessage)
	}
}

func TestTransaction_TransitionsFromTerminalStatusAreRejected(t *testing.T) {
	terminalStates := map[domain.TransactionStatus]func(*domain.Transaction) error{
		domain.TransactionStatusExecuted: func(tx *domain.Transaction) error {
			return tx.MarkExecuted("txn-1", domain.NewMoneyFromCents(100), testResolvedAt)
		},
		domain.TransactionStatusRejected: func(tx *domain.Transaction) error {
			return tx.MarkRejected("CODE", "message", testResolvedAt)
		},
		domain.TransactionStatusFailed: func(tx *domain.Transaction) error {
			return tx.MarkFailed("CODE", "message", testResolvedAt)
		},
	}

	for from, reach := range terminalStates {
		for to, transition := range terminalStates {
			t.Run(string(from)+" to "+string(to), func(t *testing.T) {
				transaction := newPendingTransaction(t)
				if err := reach(transaction); err != nil {
					t.Fatalf("could not reach %s: %v", from, err)
				}
				snapshot := *transaction

				err := transition(transaction)

				if !errors.Is(err, domain.ErrInvalidStatusTransition) {
					t.Fatalf("error = %v, want ErrInvalidStatusTransition", err)
				}
				var transitionErr *domain.InvalidTransitionError
				if !errors.As(err, &transitionErr) {
					t.Fatalf("error %v is not an *InvalidTransitionError", err)
				}
				if transitionErr.From != from || transitionErr.To != to {
					t.Errorf("transition = %s -> %s, want %s -> %s",
						transitionErr.From, transitionErr.To, from, to)
				}
				if *transaction != snapshot {
					t.Error("a rejected transition must leave the transaction unchanged")
				}
			})
		}
	}
}

func TestTransactionStatus_IsValid(t *testing.T) {
	valid := []domain.TransactionStatus{
		domain.TransactionStatusPending,
		domain.TransactionStatusExecuted,
		domain.TransactionStatusRejected,
		domain.TransactionStatusFailed,
	}
	for _, status := range valid {
		if !status.IsValid() {
			t.Errorf("%q should be a valid status", status)
		}
	}

	for _, status := range []domain.TransactionStatus{"", "APPROVED", "executed"} {
		if status.IsValid() {
			t.Errorf("%q should not be a valid status", status)
		}
	}
}

func assertValidationField(t *testing.T, err error, wantField string) {
	t.Helper()
	var validationErr *domain.ValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("error %v is not a *ValidationError", err)
	}
	if validationErr.Field != wantField {
		t.Errorf("ValidationError.Field = %q, want %q", validationErr.Field, wantField)
	}
}
