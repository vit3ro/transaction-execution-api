package providerclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/sony/gobreaker/v2"

	"github.com/vit3ro/transaction-execution-api/internal/ports"
)

const (
	breakerName      = "provider"
	keepAlivePeriod  = 30 * time.Second
	maxResponseBytes = 1 << 20
)

var _ ports.ProviderClient = (*Client)(nil)

type Client struct {
	endpoint       string
	requestTimeout time.Duration
	retry          retryPolicy
	httpClient     *http.Client
	breaker        *gobreaker.CircuitBreaker[ports.ProviderResult]
	logger         *slog.Logger
}

func New(config Config, logger *slog.Logger) (*Client, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	endpoint, err := config.executeURL()
	if err != nil {
		return nil, err
	}

	return &Client{
		endpoint:       endpoint,
		requestTimeout: config.RequestTimeout,
		retry: retryPolicy{
			maxRetries: config.MaxRetries,
			baseDelay:  config.RetryBaseDelay,
			maxDelay:   config.RetryMaxDelay,
		},
		httpClient: newHTTPClient(config),
		breaker:    newBreaker(config, logger),
		logger:     logger,
	}, nil
}

func (c *Client) Execute(ctx context.Context, request ports.ProviderRequest) (ports.ProviderResult, error) {
	payload, err := encodeRequest(request)
	if err != nil {
		return ports.ProviderResult{}, fmt.Errorf("encode provider request: %w", err)
	}

	// One deadline for the whole call, retries and waits included, so the worst
	// case latency is known.
	ctx, cancel := context.WithTimeout(ctx, c.requestTimeout)
	defer cancel()

	result, err := c.breaker.Execute(func() (ports.ProviderResult, error) {
		return c.sendWithRetry(ctx, payload)
	})
	if errors.Is(err, gobreaker.ErrOpenState) || errors.Is(err, gobreaker.ErrTooManyRequests) {
		return ports.ProviderResult{}, fmt.Errorf("%w: %w", ErrCircuitOpen, err)
	}
	return result, err
}

func (c *Client) sendWithRetry(ctx context.Context, payload []byte) (ports.ProviderResult, error) {
	for attempt := 0; ; attempt++ {
		result, err := c.send(ctx, payload)
		if err == nil || !c.retry.allows(attempt, err) {
			return result, err
		}

		c.logger.WarnContext(ctx, "retrying provider request after connection failure",
			"retry", attempt+1,
			"maxRetries", c.retry.maxRetries,
			"error", err,
		)
		if waitErr := wait(ctx, c.retry.backoff(attempt)); waitErr != nil {
			return ports.ProviderResult{}, fmt.Errorf("%w: %w", ErrRequestFailed, waitErr)
		}
	}
}

func (c *Client) send(ctx context.Context, payload []byte) (ports.ProviderResult, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return ports.ProviderResult{}, fmt.Errorf("build provider request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")

	response, err := c.httpClient.Do(request)
	if err != nil {
		return ports.ProviderResult{}, fmt.Errorf("%w: %w", ErrRequestFailed, err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return ports.ProviderResult{}, fmt.Errorf("%w: read response body: %w", ErrRequestFailed, err)
	}
	return decodeResponse(response.StatusCode, body)
}

func newHTTPClient(config Config) *http.Client {
	dialer := &net.Dialer{
		Timeout:   config.DialTimeout,
		KeepAlive: keepAlivePeriod,
	}
	return &http.Client{
		Transport: &http.Transport{
			DialContext:         dialer.DialContext,
			TLSHandshakeTimeout: config.DialTimeout,
			MaxIdleConns:        config.MaxIdleConns,
			// Go keeps only 2 idle connections per host by default. With a single
			// provider host that would mean reconnecting on almost every request.
			MaxIdleConnsPerHost: config.MaxIdleConnsPerHost,
			IdleConnTimeout:     config.IdleConnTimeout,
			ForceAttemptHTTP2:   true,
		},
		// Never follow a redirect on a request that moves money.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func newBreaker(config Config, logger *slog.Logger) *gobreaker.CircuitBreaker[ports.ProviderResult] {
	return gobreaker.NewCircuitBreaker[ports.ProviderResult](gobreaker.Settings{
		Name:        breakerName,
		MaxRequests: config.BreakerHalfOpenMaxRequests,
		Timeout:     config.BreakerOpenTimeout,
		// A business rejection returns no error, so it counts as a success: the
		// provider is working. Only technical failures open the breaker.
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			return counts.ConsecutiveFailures >= config.BreakerFailureThreshold
		},
		// A client that hangs up says nothing about the provider health.
		IsExcluded: func(err error) bool {
			return errors.Is(err, context.Canceled)
		},
		OnStateChange: func(name string, from, to gobreaker.State) {
			logger.Warn("provider circuit breaker changed state",
				"breaker", name,
				"from", from.String(),
				"to", to.String(),
			)
		},
	})
}
