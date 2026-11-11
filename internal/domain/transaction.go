package domain

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

type TransactionType string

const (
	TransactionTypeCredit TransactionType = "CREDIT"
	TransactionTypeDebit  TransactionType = "DEBIT"
)

func (t TransactionType) IsValid() bool {
	return t == TransactionTypeCredit || t == TransactionTypeDebit
}

type TransactionStatus string

const (
	TransactionStatusPending  TransactionStatus = "PENDING"
	TransactionStatusExecuted TransactionStatus = "EXECUTED"
	TransactionStatusRejected TransactionStatus = "REJECTED"
	TransactionStatusFailed   TransactionStatus = "FAILED"
)

func (s TransactionStatus) IsValid() bool {
	switch s {
	case TransactionStatusPending, TransactionStatusExecuted, TransactionStatusRejected, TransactionStatusFailed:
		return true
	default:
		return false
	}
}

type Currency string

const CurrencyMXN Currency = "MXN"

func (c Currency) IsSupported() bool {
	return c == CurrencyMXN
}

const (
	MaxDescriptionLength    = 255
	MaxIdempotencyKeyLength = 255
)

var (
	// The minimum is exclusive (1.00 is rejected, 1.01 passes) and the debit
	// limit is inclusive (10,000.00 passes, 10,000.01 is rejected).
	minAmountExclusive = NewMoneyFromCents(1_00)
	maxDebitAmount     = NewMoneyFromCents(10_000_00)
)

type Transaction struct {
	ID                    uuid.UUID
	AccountID             string
	Type                  TransactionType
	Amount                Money
	Currency              Currency
	Description           string
	Status                TransactionStatus
	ProviderTransactionID string
	// Pointer because 0.00 is a valid balance; nil means the provider gave none.
	BalanceAfter     *Money
	RejectionCode    string
	RejectionMessage string
	IdempotencyKey   string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type NewTransactionParams struct {
	ID             uuid.UUID
	AccountID      string
	Type           TransactionType
	Amount         Money
	Currency       Currency
	Description    string
	IdempotencyKey string
	CreatedAt      time.Time
}

func NewTransaction(params NewTransactionParams) (*Transaction, error) {
	if err := validate(params); err != nil {
		return nil, err
	}

	createdAt := normalizeTime(params.CreatedAt)
	return &Transaction{
		ID:             params.ID,
		AccountID:      params.AccountID,
		Type:           params.Type,
		Amount:         params.Amount,
		Currency:       params.Currency,
		Description:    params.Description,
		Status:         TransactionStatusPending,
		IdempotencyKey: params.IdempotencyKey,
		CreatedAt:      createdAt,
		UpdatedAt:      createdAt,
	}, nil
}

func (t *Transaction) MarkExecuted(providerTransactionID string, balanceAfter Money, at time.Time) error {
	if err := t.transitionTo(TransactionStatusExecuted, at); err != nil {
		return err
	}
	t.ProviderTransactionID = providerTransactionID
	t.BalanceAfter = &balanceAfter
	return nil
}

func (t *Transaction) MarkRejected(code, message string, at time.Time) error {
	if err := t.transitionTo(TransactionStatusRejected, at); err != nil {
		return err
	}
	t.RejectionCode = code
	t.RejectionMessage = message
	return nil
}

func (t *Transaction) MarkFailed(code, message string, at time.Time) error {
	if err := t.transitionTo(TransactionStatusFailed, at); err != nil {
		return err
	}
	t.RejectionCode = code
	t.RejectionMessage = message
	return nil
}

// Only PENDING can move. Final states are immutable, so a late or repeated
// provider answer can never rewrite a transaction that is already resolved.
func (t *Transaction) transitionTo(next TransactionStatus, at time.Time) error {
	if t.Status != TransactionStatusPending {
		return &InvalidTransitionError{From: t.Status, To: next}
	}
	t.Status = next
	t.UpdatedAt = normalizeTime(at)
	return nil
}

func validate(params NewTransactionParams) error {
	if strings.TrimSpace(params.AccountID) == "" {
		return newValidationError("accountId", ErrAccountIDRequired)
	}
	if !params.Type.IsValid() {
		return newValidationError("type", ErrInvalidTransactionType)
	}
	if !params.Currency.IsSupported() {
		return newValidationError("currency", ErrUnsupportedCurrency)
	}
	if !params.Amount.GreaterThan(minAmountExclusive) {
		return newValidationError("amount", ErrAmountBelowMinimum)
	}
	if params.Type == TransactionTypeDebit && params.Amount.GreaterThan(maxDebitAmount) {
		return newValidationError("amount", ErrDebitAmountExceedsLimit)
	}
	if utf8.RuneCountInString(params.Description) > MaxDescriptionLength {
		return newValidationError("description", ErrDescriptionTooLong)
	}
	if utf8.RuneCountInString(params.IdempotencyKey) > MaxIdempotencyKeyLength {
		return newValidationError("idempotencyKey", ErrIdempotencyKeyTooLong)
	}
	return nil
}

// PostgreSQL stores microseconds. Truncating here keeps what we return equal
// to what we read back from the database.
func normalizeTime(value time.Time) time.Time {
	return value.UTC().Truncate(time.Microsecond)
}
