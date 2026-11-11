package httpapi_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/vit3ro/transaction-execution-api/internal/application"
	"github.com/vit3ro/transaction-execution-api/internal/domain"
)

func TestCreateTransaction_Executed(t *testing.T) {
	f := newFixture()
	f.executor.result = application.ExecuteTransactionResult{Transaction: executedTransaction(t)}

	recorder := f.post(validBody)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", recorder.Code, recorder.Body.String())
	}
	fields := decodeFields(t, recorder)
	assertFields(t, fields, map[string]string{
		"id":                    `"0192f0c1-7c3a-7b1e-8f00-000000000001"`,
		"accountId":             `"acc-123456"`,
		"type":                  `"CREDIT"`,
		"amount":                `1500.00`,
		"currency":              `"MXN"`,
		"description":           `"Transferencia recibida"`,
		"status":                `"EXECUTED"`,
		"providerTransactionId": `"txn-789"`,
		"balanceAfter":          `5500.00`,
		"createdAt":             `"2025-03-15T10:30:00Z"`,
	})
	for _, absent := range []string{"rejectionCode", "rejectionMessage", "idempotencyKey", "updatedAt"} {
		if _, present := fields[absent]; present {
			t.Errorf("field %s must not be part of an executed response", absent)
		}
	}

	if len(f.executor.inputs) != 1 {
		t.Fatalf("use case calls = %d, want 1", len(f.executor.inputs))
	}
	wantInput := application.ExecuteTransactionInput{
		AccountID:   "acc-123456",
		Type:        domain.TransactionTypeCredit,
		Amount:      domain.NewMoneyFromCents(150000),
		Currency:    domain.CurrencyMXN,
		Description: "Transferencia recibida",
	}
	if f.executor.inputs[0] != wantInput {
		t.Errorf("use case input = %+v, want %+v", f.executor.inputs[0], wantInput)
	}
}

func TestCreateTransaction_AcceptsAmountFormats(t *testing.T) {
	amounts := map[string]int64{
		`1500`:    150000,
		`1500.5`:  150050,
		`1500.50`: 150050,
		`0.01`:    1,
	}

	for literal, wantCents := range amounts {
		t.Run(literal, func(t *testing.T) {
			f := newFixture()
			f.executor.result = application.ExecuteTransactionResult{Transaction: executedTransaction(t)}
			body := fmt.Sprintf(`{"accountId":"acc-1","type":"DEBIT","amount":%s,"currency":"MXN"}`, literal)

			recorder := f.post(body)

			if recorder.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201 (body %s)", recorder.Code, recorder.Body.String())
			}
			if got := f.executor.inputs[0].Amount.Cents(); got != wantCents {
				t.Errorf("amount = %d cents, want %d", got, wantCents)
			}
			if f.executor.inputs[0].Description != "" {
				t.Errorf("description = %q, want it empty when omitted", f.executor.inputs[0].Description)
			}
		})
	}
}

func TestCreateTransaction_RejectedByProviderIsStillCreated(t *testing.T) {
	f := newFixture()
	f.executor.result = application.ExecuteTransactionResult{Transaction: rejectedTransaction(t)}

	recorder := f.post(validBody)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", recorder.Code, recorder.Body.String())
	}
	fields := decodeFields(t, recorder)
	assertFields(t, fields, map[string]string{
		"status":           `"REJECTED"`,
		"rejectionCode":    `"INSUFFICIENT_FUNDS"`,
		"rejectionMessage": `"Account balance is not enough"`,
	})
	for _, absent := range []string{"providerTransactionId", "balanceAfter"} {
		if _, present := fields[absent]; present {
			t.Errorf("field %s must not be part of a rejected response", absent)
		}
	}
}

func TestCreateTransaction_ProviderFailures(t *testing.T) {
	tests := []struct {
		failureCode string
		wantStatus  int
	}{
		{application.FailureCodeProviderUnavailable, http.StatusServiceUnavailable},
		{application.FailureCodeProviderInvalidResponse, http.StatusBadGateway},
		{application.FailureCodeProviderTimeout, http.StatusGatewayTimeout},
		{"SOMETHING_NEW", http.StatusServiceUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.failureCode, func(t *testing.T) {
			f := newFixture()
			f.executor.result = application.ExecuteTransactionResult{Transaction: failedTransaction(t, tt.failureCode)}

			recorder := f.post(validBody)

			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, tt.wantStatus)
			}
			assertFields(t, decodeFields(t, recorder), map[string]string{
				"id":            `"0192f0c1-7c3a-7b1e-8f00-000000000001"`,
				"status":        `"FAILED"`,
				"rejectionCode": fmt.Sprintf("%q", tt.failureCode),
			})
		})
	}
}

func TestCreateTransaction_BusinessRules(t *testing.T) {
	tests := []struct {
		name     string
		rule     error
		field    string
		wantCode string
	}{
		{"minimum amount", domain.ErrAmountBelowMinimum, "amount", "AMOUNT_BELOW_MINIMUM"},
		{"debit limit", domain.ErrDebitAmountExceedsLimit, "amount", "DEBIT_AMOUNT_EXCEEDS_LIMIT"},
		{"unsupported currency", domain.ErrUnsupportedCurrency, "currency", "UNSUPPORTED_CURRENCY"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture()
			f.executor.err = &domain.ValidationError{Field: tt.field, Rule: tt.rule}

			recorder := f.post(validBody)

			problem := assertProblem(t, recorder, http.StatusUnprocessableEntity, tt.wantCode)
			if problem.Detail != tt.rule.Error() {
				t.Errorf("detail = %q, want %q", problem.Detail, tt.rule.Error())
			}
			if !slices.Equal(problem.fields(), []string{tt.field}) {
				t.Errorf("fields = %v, want [%s]", problem.fields(), tt.field)
			}
		})
	}
}

func TestCreateTransaction_DomainInputErrorsAreBadRequests(t *testing.T) {
	rules := map[string]error{
		"description":    domain.ErrDescriptionTooLong,
		"idempotencyKey": domain.ErrIdempotencyKeyTooLong,
	}

	for field, rule := range rules {
		t.Run(field, func(t *testing.T) {
			f := newFixture()
			f.executor.err = &domain.ValidationError{Field: field, Rule: rule}

			recorder := f.post(validBody)

			problem := assertProblem(t, recorder, http.StatusBadRequest, "VALIDATION_ERROR")
			if !slices.Equal(problem.fields(), []string{field}) {
				t.Errorf("fields = %v, want [%s]", problem.fields(), field)
			}
		})
	}
}

func TestCreateTransaction_InvalidJSON(t *testing.T) {
	bodies := map[string]string{
		"malformed":       `{"accountId":`,
		"empty body":      ``,
		"plain text":      `hello`,
		"array":           `[]`,
		"trailing object": validBody + validBody,
		"trailing text":   validBody + ` garbage`,
	}

	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			f := newFixture()

			recorder := f.post(body)

			assertProblem(t, recorder, http.StatusBadRequest, "INVALID_JSON")
			if len(f.executor.inputs) != 0 {
				t.Error("the use case must not be called with an invalid body")
			}
		})
	}
}

func TestCreateTransaction_InvalidFields(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantFields []string
	}{
		{
			name:       "unknown field",
			body:       `{"accountId":"acc-1","type":"CREDIT","amount":10.00,"currency":"MXN","balance":999}`,
			wantFields: []string{"balance"},
		},
		{
			name:       "all required fields missing",
			body:       `{}`,
			wantFields: []string{"accountId", "type", "amount", "currency"},
		},
		{
			name:       "required fields set to null",
			body:       `{"accountId":null,"type":null,"amount":null,"currency":null}`,
			wantFields: []string{"accountId", "type", "amount", "currency"},
		},
		{
			name:       "blank account id",
			body:       `{"accountId":"  ","type":"CREDIT","amount":10.00,"currency":"MXN"}`,
			wantFields: []string{"accountId"},
		},
		{
			name:       "account id with the wrong type",
			body:       `{"accountId":123,"type":"CREDIT","amount":10.00,"currency":"MXN"}`,
			wantFields: []string{"accountId"},
		},
		{
			name:       "type outside the enum",
			body:       `{"accountId":"acc-1","type":"TRANSFER","amount":10.00,"currency":"MXN"}`,
			wantFields: []string{"type"},
		},
		{
			name:       "lowercase type",
			body:       `{"accountId":"acc-1","type":"credit","amount":10.00,"currency":"MXN"}`,
			wantFields: []string{"type"},
		},
		{
			name:       "amount as a string",
			body:       `{"accountId":"acc-1","type":"CREDIT","amount":"10.00","currency":"MXN"}`,
			wantFields: []string{"amount"},
		},
		{
			name:       "amount with three decimals",
			body:       `{"accountId":"acc-1","type":"CREDIT","amount":10.001,"currency":"MXN"}`,
			wantFields: []string{"amount"},
		},
		{
			name:       "amount in exponent notation",
			body:       `{"accountId":"acc-1","type":"CREDIT","amount":1e3,"currency":"MXN"}`,
			wantFields: []string{"amount"},
		},
		{
			name:       "amount as a boolean",
			body:       `{"accountId":"acc-1","type":"CREDIT","amount":true,"currency":"MXN"}`,
			wantFields: []string{"amount"},
		},
		{
			name:       "currency with an invalid format",
			body:       `{"accountId":"acc-1","type":"CREDIT","amount":10.00,"currency":"pesos"}`,
			wantFields: []string{"currency"},
		},
		{
			name:       "several invalid fields at once",
			body:       `{"accountId":"","type":"X","amount":1.999,"currency":"mxn"}`,
			wantFields: []string{"accountId", "type", "amount", "currency"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture()

			recorder := f.post(tt.body)

			problem := assertProblem(t, recorder, http.StatusBadRequest, "VALIDATION_ERROR")
			if !slices.Equal(problem.fields(), tt.wantFields) {
				t.Errorf("fields = %v, want %v", problem.fields(), tt.wantFields)
			}
			if len(f.executor.inputs) != 0 {
				t.Error("the use case must not be called with invalid fields")
			}
		})
	}
}

func TestCreateTransaction_RejectsNonJSONContentType(t *testing.T) {
	contentTypes := map[string]string{
		"missing":         "",
		"plain text":      "text/plain",
		"form":            "application/x-www-form-urlencoded",
		"similar subtype": "application/jsonx",
		"malformed":       "application/json;;;",
	}

	for name, contentType := range contentTypes {
		t.Run(name, func(t *testing.T) {
			f := newFixture()
			request := newPost(validBody)
			request.Header.Del("Content-Type")
			if contentType != "" {
				request.Header.Set("Content-Type", contentType)
			}

			recorder := f.do(request)

			assertProblem(t, recorder, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE")
			if len(f.executor.inputs) != 0 {
				t.Error("the use case must not be called with an unsupported media type")
			}
		})
	}
}

func TestCreateTransaction_AcceptsJSONContentTypeVariants(t *testing.T) {
	for _, contentType := range []string{"application/json; charset=utf-8", "Application/JSON"} {
		t.Run(contentType, func(t *testing.T) {
			f := newFixture()
			f.executor.result = application.ExecuteTransactionResult{Transaction: executedTransaction(t)}
			request := newPost(validBody)
			request.Header.Set("Content-Type", contentType)

			recorder := f.do(request)

			if recorder.Code != http.StatusCreated {
				t.Errorf("status = %d, want 201 (body %s)", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestCreateTransaction_BodyTooLarge(t *testing.T) {
	f := newFixture()
	body := fmt.Sprintf(`{"accountId":"acc-1","type":"CREDIT","amount":10.00,"currency":"MXN","description":%q}`,
		strings.Repeat("a", 70_000))

	recorder := f.post(body)

	assertProblem(t, recorder, http.StatusRequestEntityTooLarge, "REQUEST_TOO_LARGE")
	if len(f.executor.inputs) != 0 {
		t.Error("the use case must not be called with an oversized body")
	}
}

func TestCreateTransaction_UnexpectedErrorsDoNotLeakDetails(t *testing.T) {
	failures := map[string]error{
		"storage failure":       errors.New("pq: password authentication failed for user admin"),
		"outcome not persisted": fmt.Errorf("%w: connection reset", application.ErrOutcomeNotPersisted),
	}

	for name, failure := range failures {
		t.Run(name, func(t *testing.T) {
			f := newFixture()
			f.executor.err = failure

			recorder := f.post(validBody)

			problem := assertProblem(t, recorder, http.StatusInternalServerError, "INTERNAL_ERROR")
			if problem.Detail != "an unexpected error occurred" {
				t.Errorf("detail = %q, want a generic message", problem.Detail)
			}
			for _, secret := range []string{"password", "admin", "connection reset", "persisted"} {
				if strings.Contains(recorder.Body.String(), secret) {
					t.Errorf("response leaks internal detail %q: %s", secret, recorder.Body.String())
				}
			}
			if !strings.Contains(f.logs.String(), failure.Error()) {
				t.Error("the internal error must be written to the log")
			}
		})
	}
}

func TestCreateTransaction_RequestTimeout(t *testing.T) {
	f := newFixture()
	f.executor.err = fmt.Errorf("persist pending transaction: %w", context.DeadlineExceeded)

	recorder := f.post(validBody)

	assertProblem(t, recorder, http.StatusGatewayTimeout, "REQUEST_TIMEOUT")
}

func TestCreateTransaction_ForwardsIdempotencyKey(t *testing.T) {
	f := newFixture()
	f.executor.result = application.ExecuteTransactionResult{Transaction: executedTransaction(t)}
	request := newPost(validBody)
	request.Header.Set("Idempotency-Key", "order-42")

	recorder := f.do(request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", recorder.Code)
	}
	if got := f.executor.inputs[0].IdempotencyKey; got != "order-42" {
		t.Errorf("idempotency key = %q, want order-42", got)
	}
	if recorder.Header().Get("Idempotent-Replayed") != "" {
		t.Error("a first execution must not be flagged as replayed")
	}
}

func TestCreateTransaction_IdempotentReplay(t *testing.T) {
	tests := []struct {
		name        string
		transaction func(*testing.T) *domain.Transaction
		wantStatus  int
		wantState   string
	}{
		{"executed original", executedTransaction, http.StatusOK, `"EXECUTED"`},
		{"rejected original", rejectedTransaction, http.StatusOK, `"REJECTED"`},
		{
			"failed original",
			func(t *testing.T) *domain.Transaction {
				return failedTransaction(t, application.FailureCodeProviderTimeout)
			},
			http.StatusGatewayTimeout,
			`"FAILED"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture()
			f.executor.result = application.ExecuteTransactionResult{Transaction: tt.transaction(t), Replayed: true}
			request := newPost(validBody)
			request.Header.Set("Idempotency-Key", "order-42")

			recorder := f.do(request)

			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, tt.wantStatus)
			}
			if got := recorder.Header().Get("Idempotent-Replayed"); got != "true" {
				t.Errorf("Idempotent-Replayed = %q, want true", got)
			}
			assertFields(t, decodeFields(t, recorder), map[string]string{
				"id":     `"0192f0c1-7c3a-7b1e-8f00-000000000001"`,
				"status": tt.wantState,
			})
		})
	}
}

func TestCreateTransaction_IdempotentRequestStillInProgress(t *testing.T) {
	f := newFixture()
	f.executor.result = application.ExecuteTransactionResult{Transaction: pendingTransaction(t), Replayed: true}
	request := newPost(validBody)
	request.Header.Set("Idempotency-Key", "order-42")

	recorder := f.do(request)

	assertProblem(t, recorder, http.StatusConflict, "IDEMPOTENT_REQUEST_IN_PROGRESS")
}

func TestCreateTransaction_IdempotencyKeyReused(t *testing.T) {
	f := newFixture()
	f.executor.err = application.ErrIdempotencyKeyReused
	request := newPost(validBody)
	request.Header.Set("Idempotency-Key", "order-42")

	recorder := f.do(request)

	assertProblem(t, recorder, http.StatusUnprocessableEntity, "IDEMPOTENCY_KEY_REUSED")
}
