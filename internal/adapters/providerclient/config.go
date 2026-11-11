package providerclient

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

const executePath = "/provider/v1/execute"

type Config struct {
	BaseURL        string
	RequestTimeout time.Duration
	DialTimeout    time.Duration

	MaxRetries     int
	RetryBaseDelay time.Duration
	RetryMaxDelay  time.Duration

	MaxIdleConns        int
	MaxIdleConnsPerHost int
	IdleConnTimeout     time.Duration

	BreakerFailureThreshold    uint32
	BreakerOpenTimeout         time.Duration
	BreakerHalfOpenMaxRequests uint32
}

func (c Config) executeURL() (string, error) {
	base, err := url.Parse(c.BaseURL)
	if err != nil {
		return "", fmt.Errorf("%w: base url: %w", ErrInvalidConfig, err)
	}
	if base.Scheme == "" || base.Host == "" {
		return "", fmt.Errorf("%w: base url %q must include scheme and host", ErrInvalidConfig, c.BaseURL)
	}
	return strings.TrimRight(c.BaseURL, "/") + executePath, nil
}

func (c Config) validate() error {
	if c.RequestTimeout <= 0 {
		return fmt.Errorf("%w: request timeout must be positive", ErrInvalidConfig)
	}
	return nil
}
