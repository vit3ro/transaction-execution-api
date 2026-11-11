package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	testInitialBalance = 1000000
	testSlowDelay      = 40 * time.Millisecond
)

func newTestProvider() *provider {
	p := newProvider(testInitialBalance, testSlowDelay, slog.New(slog.DiscardHandler))
	p.now = func() time.Time { return time.Date(2025, 3, 15, 10, 30, 0, 0, time.UTC) }
	return p
}

func execute(t *testing.T, p *provider, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/provider/v1/execute", strings.NewReader(body))
	p.routes().ServeHTTP(recorder, request)
	return recorder
}

func requestBody(accountID, transactionType, amount string) string {
	return fmt.Sprintf(`{"accountId":%q,"type":%q,"amount":%s,"currency":"MXN"}`, accountID, transactionType, amount)
}

func decodeBody(t *testing.T, recorder *httptest.ResponseRecorder) map[string]json.RawMessage {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &fields); err != nil {
		t.Fatalf("response %q is not valid JSON: %v", recorder.Body.String(), err)
	}
	return fields
}

func TestExecute_ApprovedKeepsCoherentBalance(t *testing.T) {
	p := newTestProvider()
	steps := []struct {
		transactionType string
		amount          string
		wantBalance     string
	}{
		{"CREDIT", "1500.00", "11500.00"},
		{"DEBIT", "500.50", "10999.50"},
		{"CREDIT", "0.50", "11000.00"},
	}

	for index, step := range steps {
		recorder := execute(t, p, requestBody("acc-123456", step.transactionType, step.amount))

		if recorder.Code != http.StatusOK {
			t.Fatalf("step %d status = %d, want 200 (body %s)", index, recorder.Code, recorder.Body.String())
		}
		fields := decodeBody(t, recorder)
		if got := string(fields["status"]); got != `"APPROVED"` {
			t.Errorf("step %d status = %s, want APPROVED", index, got)
		}
		if got := string(fields["balance"]); got != step.wantBalance {
			t.Errorf("step %d balance = %s, want %s", index, got, step.wantBalance)
		}
		if got := string(fields["executedAt"]); got != `"2025-03-15T10:30:00Z"` {
			t.Errorf("step %d executedAt = %s, want an RFC 3339 UTC timestamp", index, got)
		}
		wantID := fmt.Sprintf(`"txn-%06d"`, index+1)
		if got := string(fields["transactionId"]); got != wantID {
			t.Errorf("step %d transactionId = %s, want %s", index, got, wantID)
		}
	}
}

func TestExecute_BalancesAreIsolatedPerAccount(t *testing.T) {
	p := newTestProvider()
	execute(t, p, requestBody("acc-A", "CREDIT", "100.00"))

	recorder := execute(t, p, requestBody("acc-B", "CREDIT", "1.00"))

	if got := string(decodeBody(t, recorder)["balance"]); got != "10001.00" {
		t.Errorf("balance = %s, want 10001.00", got)
	}
}

func TestExecute_DebitOverBalanceIsRejected(t *testing.T) {
	p := newTestProvider()

	recorder := execute(t, p, requestBody("acc-123456", "DEBIT", "10000.01"))

	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", recorder.Code)
	}
	if got := string(decodeBody(t, recorder)["code"]); got != `"INSUFFICIENT_FUNDS"` {
		t.Errorf("code = %s, want INSUFFICIENT_FUNDS", got)
	}

	recorder = execute(t, p, requestBody("acc-123456", "DEBIT", "10000.00"))
	if got := string(decodeBody(t, recorder)["balance"]); got != "0.00" {
		t.Errorf("balance after a rejected debit = %s, want it untouched and then 0.00", got)
	}
}

func TestExecute_SpecialAccountsWithContractBody(t *testing.T) {
	tests := []struct {
		accountID  string
		wantStatus int
		wantCode   string
	}{
		{accountInsufficient, http.StatusUnprocessableEntity, "INSUFFICIENT_FUNDS"},
		{accountBlocked, http.StatusForbidden, "ACCOUNT_BLOCKED"},
		{accountError, http.StatusInternalServerError, "INTERNAL_ERROR"},
	}

	for _, tt := range tests {
		t.Run(tt.accountID, func(t *testing.T) {
			recorder := execute(t, newTestProvider(), requestBody(tt.accountID, "CREDIT", "100.00"))

			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, tt.wantStatus)
			}
			fields := decodeBody(t, recorder)
			if got := string(fields["status"]); got != `"REJECTED"` {
				t.Errorf("status = %s, want REJECTED", got)
			}
			if got := string(fields["code"]); got != fmt.Sprintf("%q", tt.wantCode) {
				t.Errorf("code = %s, want %s", got, tt.wantCode)
			}
			if len(fields["message"]) == 0 {
				t.Error("a rejection must include a message")
			}
		})
	}
}

func TestExecute_SpecialAccountsWithoutContractBody(t *testing.T) {
	tests := []struct {
		accountID  string
		wantStatus int
	}{
		{accountDown, http.StatusServiceUnavailable},
		{accountGarbage, http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.accountID, func(t *testing.T) {
			recorder := execute(t, newTestProvider(), requestBody(tt.accountID, "CREDIT", "100.00"))

			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, tt.wantStatus)
			}
			if json.Valid(recorder.Body.Bytes()) {
				t.Errorf("body %q must not be valid JSON", recorder.Body.String())
			}
		})
	}
}

func TestExecute_SlowAccountDelaysTheResponse(t *testing.T) {
	p := newTestProvider()
	start := time.Now()

	recorder := execute(t, p, requestBody(accountSlow, "CREDIT", "100.00"))

	if elapsed := time.Since(start); elapsed < testSlowDelay {
		t.Errorf("response took %v, want at least %v", elapsed, testSlowDelay)
	}
	if recorder.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", recorder.Code)
	}
}

func TestExecute_InvalidRequests(t *testing.T) {
	bodies := map[string]string{
		"malformed JSON":     `{"accountId":`,
		"missing account":    `{"type":"CREDIT","amount":10.00,"currency":"MXN"}`,
		"unknown type":       requestBody("acc-1", "TRANSFER", "10.00"),
		"zero amount":        requestBody("acc-1", "CREDIT", "0"),
		"negative amount":    requestBody("acc-1", "CREDIT", "-5.00"),
		"three decimals":     requestBody("acc-1", "CREDIT", "10.001"),
		"missing currency":   `{"accountId":"acc-1","type":"CREDIT","amount":10.00}`,
		"amount as a string": `{"accountId":"acc-1","type":"CREDIT","amount":"abc","currency":"MXN"}`,
	}

	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			recorder := execute(t, newTestProvider(), body)

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", recorder.Code)
			}
			if got := string(decodeBody(t, recorder)["code"]); got != `"INVALID_REQUEST"` {
				t.Errorf("code = %s, want INVALID_REQUEST", got)
			}
		})
	}
}

func TestRoutes_OnlyAcceptPostOnExecute(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/provider/v1/execute", nil)

	newTestProvider().routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", recorder.Code)
	}
}

func TestParseAndFormatCents(t *testing.T) {
	values := map[string]int64{"10000.00": 1000000, "0.01": 1, "1500": 150000, "1500.5": 150050}
	for text, wantCents := range values {
		cents, err := parseCents(text)
		if err != nil || cents != wantCents {
			t.Errorf("parseCents(%q) = (%d, %v), want %d", text, cents, err, wantCents)
		}
	}
	if got := formatCents(150050); got != "1500.50" {
		t.Errorf("formatCents(150050) = %q, want 1500.50", got)
	}
}
