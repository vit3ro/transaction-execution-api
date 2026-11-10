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

func isSafeToRetry(err error) bool {
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Op == dialOperation
}

func (p retryPolicy) allows(attempt int, err error) bool {
	return attempt < p.maxRetries && isSafeToRetry(err)
}

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
