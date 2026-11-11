package providerclient

import (
	"context"
	"errors"
	"math/rand/v2"
	"net"
	"time"
)

const dialOperation = "dial"

type retryPolicy struct {
	maxRetries int
	baseDelay  time.Duration
	maxDelay   time.Duration
}

// The provider contract has no idempotency key, so retrying a request that
// may have arrived can move money twice. We only retry when the connection
// was never established (dial error): nothing was sent. Read timeouts, resets
// and 5XX are not retried because the provider may have executed it.
func isSafeToRetry(err error) bool {
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Op == dialOperation
}

func (p retryPolicy) allows(attempt int, err error) bool {
	return attempt < p.maxRetries && isSafeToRetry(err)
}

// Exponential backoff with full jitter, so many instances do not retry in
// lockstep against a provider that is already struggling.
func (p retryPolicy) backoff(attempt int) time.Duration {
	ceiling := p.baseDelay << attempt
	if ceiling <= 0 || ceiling > p.maxDelay {
		ceiling = p.maxDelay
	}
	if ceiling <= 0 {
		return 0
	}
	return rand.N(ceiling + 1)
}

func wait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
