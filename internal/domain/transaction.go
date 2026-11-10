package domain

import (
	"fmt"
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

const MaxDescriptionLength = 255

var (
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
	BalanceAfter          *Money
	RejectionCode         string
	RejectionMessage      string
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type NewTransactionParams struct {
	AccountID   string
	Type        TransactionType
	Amount      Money
	Currency    Currency
	Description string
}

func NewTransaction(params NewTransactionParams) (*Transaction, error) {
	if err := validate(params); err != nil {
		return nil, err
	}

	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("generate transaction id: %w", err)
	}

	createdAt := now()
	return &Transaction{
		ID:          id,
		AccountID:   params.AccountID,
		Type:        params.Type,
		Amount:      params.Amount,
		Currency:    params.Currency,
		Description: params.Description,
		Status:      TransactionStatusPending,
		CreatedAt:   createdAt,
		UpdatedAt:   createdAt,
	}, nil
}

func (t *Transaction) MarkExecuted(providerTransactionID string, balanceAfter Money) error {
	if err := t.transitionTo(TransactionStatusExecuted); err != nil {
		return err
	}
	t.ProviderTransactionID = providerTransactionID
	t.BalanceAfter = &balanceAfter
	return nil
}

func (t *Transaction) MarkRejected(code, message string) error {
	if err := t.transitionTo(TransactionStatusRejected); err != nil {
		return err
	}
	t.RejectionCode = code
	t.RejectionMessage = message
	return nil
}

func (t *Transaction) MarkFailed(code, message string) error {
	if err := t.transitionTo(TransactionStatusFailed); err != nil {
		return err
	}
	t.RejectionCode = code
	t.RejectionMessage = message
	return nil
}

func (t *Transaction) transitionTo(next TransactionStatus) error {
	if t.Status != TransactionStatusPending {
		return &InvalidTransitionError{From: t.Status, To: next}
	}
	t.Status = next
	t.UpdatedAt = now()
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
	return nil
}

func now() time.Time {
	return time.Now().UTC().Truncate(time.Microsecond)
}
