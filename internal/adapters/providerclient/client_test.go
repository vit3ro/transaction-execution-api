package providerclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vit3ro/transaction-execution-api/internal/adapters/providerclient"
	"github.com/vit3ro/transaction-execution-api/internal/domain"
	"github.com/vit3ro/transaction-execution-api/internal/ports"
)

const (
	approvedBody          = `{"transactionId":"txn-789","status":"APPROVED","balance":5500.00,"executedAt":"2025-03-15T10:30:00Z"}`
	insufficientFundsBody = `{"status":"REJECTED","code":"INSUFFICIENT_FUNDS","message":"Account balance is not enough"}`
)

func testConfig(baseURL string) providerclient.Config {
	return providerclient.Config{
		BaseURL:                    baseURL,
		RequestTimeout:             2 * time.Second,
		DialTimeout:                time.Second,
		MaxRetries:                 2,
		RetryBaseDelay:             time.Millisecond,
		RetryMaxDelay:              5 * time.Millisecond,
		MaxIdleConns:               10,
		MaxIdleConnsPerHost:        10,
		IdleConnTimeout:            30 * time.Second,
		BreakerFailureThreshold:    5,
		BreakerOpenTimeout:         time.Second,
		BreakerHalfOpenMaxRequests: 1,
	}
}

func newClient(t *testing.T, config providerclient.Config) *providerclient.Client {
	t.Helper()
	client, err := providerclient.New(config, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("New returned unexpected error: %v", err)
	}
	return client
}

func validRequest(t *testing.T) ports.ProviderRequest {
	t.Helper()
	amount, err := domain.ParseMoney("1500.00")
	if err != nil {
		t.Fatalf("invalid test amount: %v", err)
	}
	return ports.ProviderRequest{
		AccountID: "acc-123456",
		Type:      domain.TransactionTypeCredit,
		Amount:    amount,
		Currency:  domain.CurrencyMXN,
	}
}

func respondWith(statusCode int, body string, hits *atomic.Int32) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		_, _ = io.WriteString(w, body)
	}
}

func TestClient_Execute_Approved(t *testing.T) {
	var receivedBody []byte
	var receivedRequest *http.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedRequest = r
		receivedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, approvedBody)
	}))
	defer server.Close()
	client := newClient(t, testConfig(server.URL))

	result, err := client.Execute(context.Background(), validRequest(t))
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}

	if result.Status != ports.ProviderStatusApproved {
		t.Errorf("Status = %q, want APPROVED", result.Status)
	}
	if result.TransactionID != "txn-789" {
		t.Errorf("TransactionID = %q, want txn-789", result.TransactionID)
	}
	if result.Balance.Cents() != 550000 {
		t.Errorf("Balance = %s, want 5500.00", result.Balance)
	}
	wantExecutedAt := time.Date(2025, 3, 15, 10, 30, 0, 0, time.UTC)
	if !result.ExecutedAt.Equal(wantExecutedAt) {
		t.Errorf("ExecutedAt = %v, want %v", result.ExecutedAt, wantExecutedAt)
	}

	if receivedRequest.Method != http.MethodPost || receivedRequest.URL.Path != "/provider/v1/execute" {
		t.Errorf("request = %s %s, want POST /provider/v1/execute", receivedRequest.Method, receivedRequest.URL.Path)
	}
	if got := receivedRequest.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(receivedBody, &fields); err != nil {
		t.Fatalf("provider received invalid JSON %q: %v", receivedBody, err)
	}
	wantFields := map[string]string{
		"accountId": `"acc-123456"`,
		"type":      `"CREDIT"`,
		"amount":    `1500.00`,
		"currency":  `"MXN"`,
	}
	if len(fields) != len(wantFields) {
		t.Errorf("provider received fields %s, want exactly accountId, type, amount and currency", receivedBody)
	}
	for name, want := range wantFields {
		if got := string(fields[name]); got != want {
			t.Errorf("field %s = %s, want %s", name, got, want)
		}
	}
}

func TestClient_Execute_Rejections(t *testing.T) {
	tests := []struct {
		name        string
		statusCode  int
		body        string
		wantCode    string
		wantMessage string
	}{
		{
			name:        "422 with insufficient funds",
			statusCode:  http.StatusUnprocessableEntity,
			body:        insufficientFundsBody,
			wantCode:    "INSUFFICIENT_FUNDS",
			wantMessage: "Account balance is not enough",
		},
		{
			name:        "500 with a contract body is still a rejection",
			statusCode:  http.StatusInternalServerError,
			body:        `{"status":"REJECTED","code":"ACCOUNT_BLOCKED","message":"Account is blocked"}`,
			wantCode:    "ACCOUNT_BLOCKED",
			wantMessage: "Account is blocked",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var hits atomic.Int32
			server := httptest.NewServer(respondWith(tt.statusCode, tt.body, &hits))
			defer server.Close()
			client := newClient(t, testConfig(server.URL))

			result, err := client.Execute(context.Background(), validRequest(t))
			if err != nil {
				t.Fatalf("Execute returned unexpected error: %v", err)
			}

			if result.Status != ports.ProviderStatusRejected {
				t.Errorf("Status = %q, want REJECTED", result.Status)
			}
			if result.RejectionCode != tt.wantCode || result.RejectionMessage != tt.wantMessage {
				t.Errorf("rejection = (%q, %q), want (%q, %q)",
					result.RejectionCode, result.RejectionMessage, tt.wantCode, tt.wantMessage)
			}
			if hits.Load() != 1 {
				t.Errorf("provider calls = %d, want 1", hits.Load())
			}
		})
	}
}

func TestClient_Execute_InvalidResponses(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
	}{
		{"503 without body", http.StatusServiceUnavailable, ""},
		{"502 with an HTML body", http.StatusBadGateway, "<html>Bad Gateway</html>"},
		{"400 with a body outside the contract", http.StatusBadRequest, `{"error":"bad request"}`},
		{"422 rejected without code", http.StatusUnprocessableEntity, `{"status":"REJECTED","message":"no code"}`},
		{"200 with invalid JSON", http.StatusOK, `{"transactionId":`},
		{"200 without transaction id", http.StatusOK, `{"status":"APPROVED","balance":5500.00,"executedAt":"2025-03-15T10:30:00Z"}`},
		{"200 without balance", http.StatusOK, `{"transactionId":"txn-789","status":"APPROVED","executedAt":"2025-03-15T10:30:00Z"}`},
		{"200 with a balance of three decimals", http.StatusOK, `{"transactionId":"txn-789","status":"APPROVED","balance":5500.123,"executedAt":"2025-03-15T10:30:00Z"}`},
		{"200 with a rejected body", http.StatusOK, insufficientFundsBody},
		{"201 is outside the contract", http.StatusCreated, approvedBody},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var hits atomic.Int32
			server := httptest.NewServer(respondWith(tt.statusCode, tt.body, &hits))
			defer server.Close()
			client := newClient(t, testConfig(server.URL))

			_, err := client.Execute(context.Background(), validRequest(t))

			if !errors.Is(err, providerclient.ErrInvalidResponse) {
				t.Fatalf("Execute error = %v, want ErrInvalidResponse", err)
			}
			if hits.Load() != 1 {
				t.Errorf("provider calls = %d, want 1 because a sent request must never be retried", hits.Load())
			}
		})
	}
}

func TestClient_Execute_TimeoutIsNotRetried(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer server.Close()
	config := testConfig(server.URL)
	config.RequestTimeout = 50 * time.Millisecond
	client := newClient(t, config)

	_, err := client.Execute(context.Background(), validRequest(t))

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Execute error = %v, want it to wrap context.DeadlineExceeded", err)
	}
	if !errors.Is(err, providerclient.ErrRequestFailed) {
		t.Errorf("Execute error = %v, want ErrRequestFailed", err)
	}
	if hits.Load() != 1 {
		t.Errorf("provider calls = %d, want 1 because a timed out request may have been executed", hits.Load())
	}
}

func TestClient_Execute_ConnectionRefused(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	baseURL := server.URL
	server.Close()
	config := testConfig(baseURL)
	config.RequestTimeout = 10 * time.Second
	config.DialTimeout = 5 * time.Second
	config.MaxRetries = 0
	client := newClient(t, config)

	_, err := client.Execute(context.Background(), validRequest(t))

	if !errors.Is(err, providerclient.ErrRequestFailed) {
		t.Fatalf("Execute error = %v, want ErrRequestFailed", err)
	}
	if errors.Is(err, providerclient.ErrInvalidResponse) {
		t.Error("a connection failure must not be reported as an invalid response")
	}
}

func TestClient_Execute_CircuitOpensAfterFailuresAndRecovers(t *testing.T) {
	var hits atomic.Int32
	var healthy atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		if !healthy.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, approvedBody)
	}))
	defer server.Close()
	config := testConfig(server.URL)
	config.BreakerFailureThreshold = 3
	config.BreakerOpenTimeout = 100 * time.Millisecond
	client := newClient(t, config)

	for attempt := 1; attempt <= 3; attempt++ {
		_, err := client.Execute(context.Background(), validRequest(t))
		if !errors.Is(err, providerclient.ErrInvalidResponse) {
			t.Fatalf("attempt %d error = %v, want ErrInvalidResponse", attempt, err)
		}
	}

	_, err := client.Execute(context.Background(), validRequest(t))
	if !errors.Is(err, providerclient.ErrCircuitOpen) {
		t.Fatalf("Execute error = %v, want ErrCircuitOpen", err)
	}
	if hits.Load() != 3 {
		t.Errorf("provider calls = %d, want 3 because an open circuit must fail fast", hits.Load())
	}

	healthy.Store(true)
	time.Sleep(150 * time.Millisecond)

	for attempt := 1; attempt <= 2; attempt++ {
		result, err := client.Execute(context.Background(), validRequest(t))
		if err != nil {
			t.Fatalf("recovery attempt %d returned unexpected error: %v", attempt, err)
		}
		if result.Status != ports.ProviderStatusApproved {
			t.Errorf("recovery attempt %d status = %q, want APPROVED", attempt, result.Status)
		}
	}
}

func TestClient_Execute_RejectionsDoNotOpenTheCircuit(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(respondWith(http.StatusUnprocessableEntity, insufficientFundsBody, &hits))
	defer server.Close()
	config := testConfig(server.URL)
	config.BreakerFailureThreshold = 2
	client := newClient(t, config)

	for attempt := 1; attempt <= 5; attempt++ {
		result, err := client.Execute(context.Background(), validRequest(t))
		if err != nil {
			t.Fatalf("attempt %d returned unexpected error: %v", attempt, err)
		}
		if result.Status != ports.ProviderStatusRejected {
			t.Errorf("attempt %d status = %q, want REJECTED", attempt, result.Status)
		}
	}
	if hits.Load() != 5 {
		t.Errorf("provider calls = %d, want 5", hits.Load())
	}
}

func TestNew_RejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*providerclient.Config)
	}{
		{"empty base url", func(c *providerclient.Config) { c.BaseURL = "" }},
		{"base url without scheme", func(c *providerclient.Config) { c.BaseURL = "provider.internal:8080" }},
		{"non positive request timeout", func(c *providerclient.Config) { c.RequestTimeout = 0 }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := testConfig("http://provider.internal")
			tt.mutate(&config)

			_, err := providerclient.New(config, slog.New(slog.DiscardHandler))

			if !errors.Is(err, providerclient.ErrInvalidConfig) {
				t.Errorf("New error = %v, want ErrInvalidConfig", err)
			}
		})
	}
}
