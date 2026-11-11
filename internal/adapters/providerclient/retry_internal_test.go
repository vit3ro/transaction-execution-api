package providerclient

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/vit3ro/transaction-execution-api/internal/domain"
	"github.com/vit3ro/transaction-execution-api/internal/ports"
)

type scriptedTransport struct {
	failures int
	failWith error
	attempts int
}

func (s *scriptedTransport) RoundTrip(*http.Request) (*http.Response, error) {
	s.attempts++
	if s.attempts <= s.failures {
		return nil, s.failWith
	}
	body := `{"transactionId":"txn-789","status":"APPROVED","balance":5500.00,"executedAt":"2025-03-15T10:30:00Z"}`
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}, nil
}

func newScriptedClient(t *testing.T, transport *scriptedTransport) *Client {
	t.Helper()
	client, err := New(Config{
		BaseURL:                    "http://provider.internal",
		RequestTimeout:             2 * time.Second,
		DialTimeout:                time.Second,
		MaxRetries:                 2,
		RetryBaseDelay:             time.Millisecond,
		RetryMaxDelay:              5 * time.Millisecond,
		BreakerFailureThreshold:    5,
		BreakerOpenTimeout:         time.Second,
		BreakerHalfOpenMaxRequests: 1,
	}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("New returned unexpected error: %v", err)
	}
	client.httpClient.Transport = transport
	return client
}

func scriptedRequest() ports.ProviderRequest {
	return ports.ProviderRequest{
		AccountID: "acc-123456",
		Type:      domain.TransactionTypeDebit,
		Amount:    domain.NewMoneyFromCents(150000),
		Currency:  domain.CurrencyMXN,
	}
}

func dialError() error {
	return &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
}

func TestClient_RetriesDialErrorsUntilSuccess(t *testing.T) {
	transport := &scriptedTransport{failures: 2, failWith: dialError()}
	client := newScriptedClient(t, transport)

	result, err := client.Execute(context.Background(), scriptedRequest())
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}

	if result.Status != ports.ProviderStatusApproved {
		t.Errorf("Status = %q, want APPROVED", result.Status)
	}
	if transport.attempts != 3 {
		t.Errorf("attempts = %d, want 3 (one call plus two retries)", transport.attempts)
	}
}

func TestClient_StopsRetryingDialErrorsAtTheLimit(t *testing.T) {
	transport := &scriptedTransport{failures: 10, failWith: dialError()}
	client := newScriptedClient(t, transport)

	_, err := client.Execute(context.Background(), scriptedRequest())

	if !errors.Is(err, ErrRequestFailed) {
		t.Fatalf("Execute error = %v, want ErrRequestFailed", err)
	}
	if transport.attempts != 3 {
		t.Errorf("attempts = %d, want 3 (one call plus two retries)", transport.attempts)
	}
}

func TestClient_DoesNotRetryErrorsAfterTheRequestWasSent(t *testing.T) {
	unsafeErrors := map[string]error{
		"read error":        &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET},
		"write error":       &net.OpError{Op: "write", Net: "tcp", Err: syscall.EPIPE},
		"unexpected EOF":    io.ErrUnexpectedEOF,
		"deadline exceeded": context.DeadlineExceeded,
	}

	for name, failure := range unsafeErrors {
		t.Run(name, func(t *testing.T) {
			transport := &scriptedTransport{failures: 10, failWith: failure}
			client := newScriptedClient(t, transport)

			_, err := client.Execute(context.Background(), scriptedRequest())

			if !errors.Is(err, ErrRequestFailed) {
				t.Fatalf("Execute error = %v, want ErrRequestFailed", err)
			}
			if transport.attempts != 1 {
				t.Errorf("attempts = %d, want 1 because the request may have reached the provider", transport.attempts)
			}
		})
	}
}

func TestBackoff_GrowsExponentiallyWithinBounds(t *testing.T) {
	policy := retryPolicy{maxRetries: 5, baseDelay: 10 * time.Millisecond, maxDelay: 35 * time.Millisecond}
	ceilings := []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 35 * time.Millisecond, 35 * time.Millisecond}

	for attempt, ceiling := range ceilings {
		for range 50 {
			delay := policy.backoff(attempt)
			if delay < 0 || delay > ceiling {
				t.Fatalf("backoff(%d) = %v, want a value between 0 and %v", attempt, delay, ceiling)
			}
		}
	}
}
