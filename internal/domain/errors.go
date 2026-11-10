package domain

import (
	"errors"
	"fmt"
)

var (
	ErrAccountIDRequired       = errors.New("account id is required")
	ErrInvalidTransactionType  = errors.New("transaction type must be CREDIT or DEBIT")
	ErrUnsupportedCurrency     = errors.New("currency is not supported, only MXN is accepted")
	ErrAmountBelowMinimum      = errors.New("amount must be greater than 1.00")
	ErrDebitAmountExceedsLimit = errors.New("debit amount must not exceed 10000.00 per operation")
	ErrDescriptionTooLong      = fmt.Errorf("description must not exceed %d characters", MaxDescriptionLength)
	ErrInvalidMoneyFormat      = errors.New("invalid money format")
	ErrInvalidStatusTransition = errors.New("invalid status transition")
)

type ValidationError struct {
	Field string
	Rule  error
}

func (e *ValidationError) Error() string {
	return e.Rule.Error()
}

func (e *ValidationError) Unwrap() error {
	return e.Rule
}

func newValidationError(field string, rule error) *ValidationError {
	return &ValidationError{Field: field, Rule: rule}
}

type InvalidTransitionError struct {
	From TransactionStatus
	To   TransactionStatus
}

func (e *InvalidTransitionError) Error() string {
	return fmt.Sprintf("%s: %s -> %s", ErrInvalidStatusTransition, e.From, e.To)
}

func (e *InvalidTransitionError) Is(target error) bool {
	return target == ErrInvalidStatusTransition
}
