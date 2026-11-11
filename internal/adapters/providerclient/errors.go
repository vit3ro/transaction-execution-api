package providerclient

import (
	"errors"

	"github.com/vit3ro/transaction-execution-api/internal/ports"
)

var (
	ErrInvalidConfig   = errors.New("invalid provider client configuration")
	ErrRequestFailed   = errors.New("provider request failed")
	ErrInvalidResponse = ports.ErrProviderInvalidResponse
	ErrCircuitOpen     = errors.New("provider circuit breaker is open")
)
