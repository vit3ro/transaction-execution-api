package ports

import "errors"

var (
	ErrTransactionAlreadyExists = errors.New("transaction already exists")
	ErrTransactionNotFound      = errors.New("transaction not found")
	ErrIdempotencyKeyConflict   = errors.New("idempotency key already belongs to another transaction")
	ErrProviderInvalidResponse  = errors.New("provider response does not match the contract")
)
