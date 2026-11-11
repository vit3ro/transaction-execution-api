//go:build e2e

package e2e_test

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"
)

const (
	baseURLEnv     = "API_BASE_URL"
	defaultBaseURL = "http://localhost:8080"

	accountInsufficient = "acc-insufficient"
	accountError        = "acc-error"
	accountSlow         = "acc-slow"
)

var (
	baseURL    = defaultBaseURL
	httpClient = &http.Client{Timeout: 20 * time.Second}
)

func TestMain(m *testing.M) {
	if value := os.Getenv(baseURLEnv); value != "" {
		baseURL = value
	}
	if err := waitUntilReady(30 * time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "API at %s is not ready: %v\nStart the environment with: docker compose up -d --build --wait\n", baseURL, err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func waitUntilReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		response, err := httpClient.Get(baseURL + "/readyz")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
			err = fmt.Errorf("readyz returned %d", response.StatusCode)
		}
		lastErr = err
		time.Sleep(500 * time.Millisecond)
	}
	return lastErr
}

type transactionRequest struct {
	AccountID   string      `json:"accountId"`
	Type        string      `json:"type"`
	Amount      json.Number `json:"amount"`
	Currency    string      `json:"currency"`
	Description string      `json:"description,omitempty"`
}

type apiResponse struct {
	status int
	header http.Header
	fields map[string]json.RawMessage
}

func (r apiResponse) text(field string) string {
	var value string
	_ = json.Unmarshal(r.fields[field], &value)
	return value
}

func (r apiResponse) number(field string) string {
	return string(r.fields[field])
}

func uniqueAccount(t *testing.T) string {
	t.Helper()
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatalf("generate account id: %v", err)
	}
	return "acc-e2e-" + hex.EncodeToString(suffix)
}

func send(t *testing.T, request *http.Request) apiResponse {
	t.Helper()
	response, err := httpClient.Do(request)
	if err != nil {
		t.Fatalf("%s %s failed: %v", request.Method, request.URL, err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	result := apiResponse{status: response.StatusCode, header: response.Header}
	if err := json.Unmarshal(body, &result.fields); err != nil {
		t.Fatalf("%s %s returned a non JSON body %q: %v", request.Method, request.URL, body, err)
	}
	if response.Header.Get("X-Request-ID") == "" {
		t.Errorf("%s %s did not return X-Request-ID", request.Method, request.URL)
	}
	return result
}

func createTransaction(t *testing.T, payload transactionRequest, headers map[string]string) apiResponse {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	request, err := http.NewRequest(http.MethodPost, baseURL+"/transactions", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	return send(t, request)
}

type transactionList struct {
	Data  []map[string]json.RawMessage `json:"data"`
	Page  int                          `json:"page"`
	Limit int                          `json:"limit"`
	Total int                          `json:"total"`
}

func listTransactions(t *testing.T, query url.Values) (int, transactionList) {
	t.Helper()
	response, err := httpClient.Get(baseURL + "/transactions?" + query.Encode())
	if err != nil {
		t.Fatalf("list transactions: %v", err)
	}
	defer response.Body.Close()

	var list transactionList
	if response.StatusCode == http.StatusOK {
		if err := json.NewDecoder(response.Body).Decode(&list); err != nil {
			t.Fatalf("decode transaction list: %v", err)
		}
	}
	return response.StatusCode, list
}

func assertStatus(t *testing.T, response apiResponse, want int) {
	t.Helper()
	if response.status != want {
		t.Fatalf("status = %d, want %d (body %s)", response.status, want, response.fields)
	}
}

func assertNotStored(t *testing.T, accountID string) {
	t.Helper()
	status, list := listTransactions(t, url.Values{"accountId": {accountID}})
	if status != http.StatusOK || list.Total != 0 {
		t.Errorf("account %s has %d stored transactions (status %d), want none", accountID, list.Total, status)
	}
}

func assertStored(t *testing.T, id, accountID, wantStatus string) {
	t.Helper()
	_, list := listTransactions(t, url.Values{"accountId": {accountID}, "status": {wantStatus}, "limit": {"100"}})
	for _, item := range list.Data {
		var storedID string
		_ = json.Unmarshal(item["id"], &storedID)
		if storedID == id {
			return
		}
	}
	t.Errorf("transaction %s was not found for account %s with status %s", id, accountID, wantStatus)
}

func TestCreditAndDebitAreExecutedWithProviderBalance(t *testing.T) {
	account := uniqueAccount(t)

	credit := createTransaction(t, transactionRequest{
		AccountID: account, Type: "CREDIT", Amount: "1500.00", Currency: "MXN", Description: "Transferencia recibida",
	}, nil)
	assertStatus(t, credit, http.StatusCreated)
	if credit.text("status") != "EXECUTED" || credit.text("providerTransactionId") == "" {
		t.Errorf("credit = %s, want an EXECUTED transaction with a provider id", credit.fields)
	}
	if credit.number("amount") != "1500.00" || credit.number("balanceAfter") != "11500.00" {
		t.Errorf("credit amount = %s and balance = %s, want 1500.00 and 11500.00", credit.number("amount"), credit.number("balanceAfter"))
	}
	if _, err := time.Parse(time.RFC3339, credit.text("createdAt")); err != nil {
		t.Errorf("createdAt = %q, want an RFC 3339 timestamp", credit.text("createdAt"))
	}

	debit := createTransaction(t, transactionRequest{
		AccountID: account, Type: "DEBIT", Amount: "250.75", Currency: "MXN", Description: "Pago de servicio",
	}, nil)
	assertStatus(t, debit, http.StatusCreated)
	if debit.text("status") != "EXECUTED" || debit.number("balanceAfter") != "11249.25" {
		t.Errorf("debit = %s, want EXECUTED with balance 11249.25", debit.fields)
	}

	assertStored(t, credit.text("id"), account, "EXECUTED")
	assertStored(t, debit.text("id"), account, "EXECUTED")
}

func TestBusinessRulesRejectBeforeCallingTheProvider(t *testing.T) {
	tests := []struct {
		name     string
		txType   string
		amount   json.Number
		currency string
		wantCode string
	}{
		{"debit over the limit", "DEBIT", "10000.01", "MXN", "DEBIT_AMOUNT_EXCEEDS_LIMIT"},
		{"unsupported currency", "CREDIT", "100.00", "USD", "UNSUPPORTED_CURRENCY"},
		{"amount at the minimum", "CREDIT", "1.00", "MXN", "AMOUNT_BELOW_MINIMUM"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account := uniqueAccount(t)

			response := createTransaction(t, transactionRequest{
				AccountID: account, Type: tt.txType, Amount: tt.amount, Currency: tt.currency,
			}, nil)

			assertStatus(t, response, http.StatusUnprocessableEntity)
			if got := response.header.Get("Content-Type"); got != "application/problem+json" {
				t.Errorf("Content-Type = %q, want application/problem+json", got)
			}
			if response.text("code") != tt.wantCode {
				t.Errorf("code = %q, want %q", response.text("code"), tt.wantCode)
			}
			assertNotStored(t, account)
		})
	}
}

func TestBoundaryAmountsAreAccepted(t *testing.T) {
	account := uniqueAccount(t)

	justOverMinimum := createTransaction(t, transactionRequest{AccountID: account, Type: "CREDIT", Amount: "1.01", Currency: "MXN"}, nil)
	assertStatus(t, justOverMinimum, http.StatusCreated)

	debitAtLimit := createTransaction(t, transactionRequest{AccountID: account, Type: "DEBIT", Amount: "10000.00", Currency: "MXN"}, nil)
	assertStatus(t, debitAtLimit, http.StatusCreated)
	if debitAtLimit.text("status") != "EXECUTED" || debitAtLimit.number("balanceAfter") != "1.01" {
		t.Errorf("debit at the limit = %s, want EXECUTED with balance 1.01", debitAtLimit.fields)
	}
}

func TestInsufficientFundsIsStoredAsRejected(t *testing.T) {
	response := createTransaction(t, transactionRequest{
		AccountID: accountInsufficient, Type: "DEBIT", Amount: "500.00", Currency: "MXN",
	}, nil)

	assertStatus(t, response, http.StatusCreated)
	if response.text("status") != "REJECTED" || response.text("rejectionCode") != "INSUFFICIENT_FUNDS" {
		t.Errorf("response = %s, want REJECTED with INSUFFICIENT_FUNDS", response.fields)
	}
	if _, present := response.fields["balanceAfter"]; present {
		t.Error("a rejected transaction must not report a balance")
	}
	assertStored(t, response.text("id"), accountInsufficient, "REJECTED")
}

func TestProviderErrorWithContractBodyIsStoredAsRejected(t *testing.T) {
	response := createTransaction(t, transactionRequest{
		AccountID: accountError, Type: "CREDIT", Amount: "100.00", Currency: "MXN",
	}, nil)

	assertStatus(t, response, http.StatusCreated)
	if response.text("status") != "REJECTED" || response.text("rejectionCode") != "INTERNAL_ERROR" {
		t.Errorf("response = %s, want REJECTED with INTERNAL_ERROR", response.fields)
	}
	assertStored(t, response.text("id"), accountError, "REJECTED")
}

func TestProviderTimeoutIsStoredAsFailed(t *testing.T) {
	start := time.Now()

	response := createTransaction(t, transactionRequest{
		AccountID: accountSlow, Type: "CREDIT", Amount: "100.00", Currency: "MXN",
	}, nil)

	assertStatus(t, response, http.StatusGatewayTimeout)
	if response.text("status") != "FAILED" || response.text("rejectionCode") != "PROVIDER_TIMEOUT" {
		t.Errorf("response = %s, want FAILED with PROVIDER_TIMEOUT", response.fields)
	}
	if elapsed := time.Since(start); elapsed > 4500*time.Millisecond {
		t.Errorf("request took %v, want the API to give up before the provider answers", elapsed)
	}
	assertStored(t, response.text("id"), accountSlow, "FAILED")
}

func TestIdempotencyKeyReturnsTheOriginalTransaction(t *testing.T) {
	account := uniqueAccount(t)
	headers := map[string]string{"Idempotency-Key": account}
	payload := transactionRequest{AccountID: account, Type: "CREDIT", Amount: "300.00", Currency: "MXN"}

	first := createTransaction(t, payload, headers)
	assertStatus(t, first, http.StatusCreated)

	replay := createTransaction(t, payload, headers)
	assertStatus(t, replay, http.StatusOK)
	if replay.text("id") != first.text("id") || replay.header.Get("Idempotent-Replayed") != "true" {
		t.Errorf("replay = %s, want the original transaction flagged as replayed", replay.fields)
	}

	payload.Amount = "999.00"
	reused := createTransaction(t, payload, headers)
	assertStatus(t, reused, http.StatusUnprocessableEntity)
	if reused.text("code") != "IDEMPOTENCY_KEY_REUSED" {
		t.Errorf("code = %q, want IDEMPOTENCY_KEY_REUSED", reused.text("code"))
	}

	if _, list := listTransactions(t, url.Values{"accountId": {account}}); list.Total != 1 {
		t.Errorf("stored transactions = %d, want exactly 1", list.Total)
	}
}

func TestListFiltersAndPagination(t *testing.T) {
	account := uniqueAccount(t)
	steps := []transactionRequest{
		{AccountID: account, Type: "CREDIT", Amount: "100.00", Currency: "MXN"},
		{AccountID: account, Type: "CREDIT", Amount: "200.00", Currency: "MXN"},
		{AccountID: account, Type: "DEBIT", Amount: "10000.00", Currency: "MXN"},
		{AccountID: account, Type: "DEBIT", Amount: "500.00", Currency: "MXN"},
	}
	ids := make([]string, 0, len(steps))
	for _, step := range steps {
		response := createTransaction(t, step, nil)
		assertStatus(t, response, http.StatusCreated)
		ids = append(ids, response.text("id"))
	}

	t.Run("by account returns newest first", func(t *testing.T) {
		status, list := listTransactions(t, url.Values{"accountId": {account}})
		if status != http.StatusOK || list.Total != 4 || len(list.Data) != 4 {
			t.Fatalf("status = %d, total = %d, items = %d, want 200, 4 and 4", status, list.Total, len(list.Data))
		}
		if list.Page != 1 || list.Limit != 20 {
			t.Errorf("pagination = (%d, %d), want the defaults (1, 20)", list.Page, list.Limit)
		}
		for position, item := range list.Data {
			var id string
			_ = json.Unmarshal(item["id"], &id)
			if want := ids[len(ids)-1-position]; id != want {
				t.Errorf("position %d = %s, want %s", position, id, want)
			}
		}
	})

	t.Run("by type", func(t *testing.T) {
		_, list := listTransactions(t, url.Values{"accountId": {account}, "type": {"CREDIT"}})
		if list.Total != 2 {
			t.Errorf("credits = %d, want 2", list.Total)
		}
	})

	t.Run("by status", func(t *testing.T) {
		_, executed := listTransactions(t, url.Values{"accountId": {account}, "status": {"EXECUTED"}})
		_, rejected := listTransactions(t, url.Values{"accountId": {account}, "status": {"REJECTED"}})
		if executed.Total != 3 || rejected.Total != 1 {
			t.Errorf("executed = %d and rejected = %d, want 3 and 1", executed.Total, rejected.Total)
		}
	})

	t.Run("by type and status", func(t *testing.T) {
		_, list := listTransactions(t, url.Values{"accountId": {account}, "type": {"DEBIT"}, "status": {"REJECTED"}})
		if list.Total != 1 || len(list.Data) != 1 {
			t.Fatalf("rejected debits = %d, want 1", list.Total)
		}
		if string(list.Data[0]["rejectionCode"]) != `"INSUFFICIENT_FUNDS"` {
			t.Errorf("rejectionCode = %s, want INSUFFICIENT_FUNDS", list.Data[0]["rejectionCode"])
		}
	})

	t.Run("pagination", func(t *testing.T) {
		seen := map[string]bool{}
		for page := 1; page <= 2; page++ {
			_, list := listTransactions(t, url.Values{"accountId": {account}, "limit": {"3"}, "page": {fmt.Sprint(page)}})
			if list.Total != 4 || list.Page != page || list.Limit != 3 {
				t.Errorf("page %d metadata = (total %d, page %d, limit %d), want (4, %d, 3)", page, list.Total, list.Page, list.Limit, page)
			}
			for _, item := range list.Data {
				seen[string(item["id"])] = true
			}
		}
		if len(seen) != 4 {
			t.Errorf("pages returned %d distinct transactions, want 4", len(seen))
		}
	})

	t.Run("invalid pagination", func(t *testing.T) {
		for _, query := range []url.Values{{"page": {"0"}}, {"limit": {"101"}}, {"page": {"abc"}}, {"status": {"APPROVED"}}} {
			if status, _ := listTransactions(t, query); status != http.StatusBadRequest {
				t.Errorf("query %s status = %d, want 400", query.Encode(), status)
			}
		}
	})
}

func TestMalformedRequestsAreBadRequests(t *testing.T) {
	bodies := map[string]string{
		"invalid JSON":   `{"accountId":`,
		"unknown field":  `{"accountId":"acc-1","type":"CREDIT","amount":10.00,"currency":"MXN","balance":1}`,
		"three decimals": `{"accountId":"acc-1","type":"CREDIT","amount":10.001,"currency":"MXN"}`,
		"missing fields": `{}`,
	}

	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			request, err := http.NewRequest(http.MethodPost, baseURL+"/transactions", bytes.NewReader([]byte(body)))
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			request.Header.Set("Content-Type", "application/json")

			response := send(t, request)

			assertStatus(t, response, http.StatusBadRequest)
		})
	}
}

func TestEveryErrorUsesProblemDetails(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		path        string
		contentType string
		body        string
		wantStatus  int
		wantCode    string
	}{
		{"non JSON content type", http.MethodPost, "/transactions", "text/plain", `{"accountId":"acc-1"}`, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE"},
		{"unknown path", http.MethodGet, "/unknown", "", "", http.StatusNotFound, "NOT_FOUND"},
		{"unsupported method", http.MethodDelete, "/transactions", "", "", http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request, err := http.NewRequest(tt.method, baseURL+tt.path, bytes.NewReader([]byte(tt.body)))
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			if tt.contentType != "" {
				request.Header.Set("Content-Type", tt.contentType)
			}

			response := send(t, request)

			assertStatus(t, response, tt.wantStatus)
			if got := response.header.Get("Content-Type"); got != "application/problem+json" {
				t.Errorf("Content-Type = %q, want application/problem+json", got)
			}
			if response.text("code") != tt.wantCode {
				t.Errorf("code = %q, want %q", response.text("code"), tt.wantCode)
			}
			if tt.wantStatus == http.StatusMethodNotAllowed && response.header.Get("Allow") == "" {
				t.Error("a 405 must list the allowed methods in the Allow header")
			}
		})
	}
}
