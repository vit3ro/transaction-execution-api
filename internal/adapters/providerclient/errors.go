package providerclient

import "errors"

var (
	ErrInvalidConfig   = errors.New("invalid provider client configuration")
	ErrRequestFailed   = errors.New("provider request failed")
	ErrInvalidResponse = errors.New("provider response does not match the contract")
	ErrCircuitOpen     = errors.New("provider circuit breaker is open")
)
