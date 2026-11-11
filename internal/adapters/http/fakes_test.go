package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	httpapi "github.com/vit3ro/transaction-execution-api/internal/adapters/http"
	"github.com/vit3ro/transaction-execution-api/internal/application"
	"github.com/vit3ro/transaction-execution-api/internal/domain"
	"github.com/vit3ro/transaction-execution-api/internal/logging"
)

const validBody = `{"accountId":"acc-123456","type":"CREDIT","amount":1500.00,"currency":"MXN","description":"Transferencia recibida"}`

var (
	fixedID  = uuid.MustParse("0192f0c1-7c3a-7b1e-8f00-000000000001")
	fixedNow = time.Date(2025, 3, 15, 10, 30, 0, 123456000, time.UTC)
)

type fakeExecutor struct {
	result application.ExecuteTransactionResult
	err    error
	inputs []application.ExecuteTransactionInput
}

func (e *fakeExecutor) Execute(_ context.Context, input application.ExecuteTransactionInput) (application.ExecuteTransactionResult, error) {
	e.inputs = append(e.inputs, input)
	return e.result, e.err
}

type fakeLister struct {
	page   application.TransactionPage
	err    error
	inputs []application.ListTransactionsInput
}

func (l *fakeLister) Execute(_ context.Context, input application.ListTransactionsInput) (application.TransactionPage, error) {
	l.inputs = append(l.inputs, input)
	return l.page, l.err
}

type fakeReadiness struct {
	err error
}

func (r fakeReadiness) Ping(context.Context) error {
	return r.err
}

type fixture struct {
	executor       *fakeExecutor
	lister         *fakeLister
	logs           *bytes.Buffer
	router         http.Handler
	requestTimeout time.Duration
}

func newRouterWithLister(f *fixture, lister httpapi.TransactionLister) http.Handler {
	return httpapi.NewRouter(httpapi.Dependencies{
		Executor:       f.executor,
		Lister:         lister,
		Readiness:      fakeReadiness{},
		Logger:         logging.New(f.logs, slog.LevelInfo),
		RequestTimeout: f.requestTimeout,
	})
}

func newFixture() *fixture {
	return newFixtureWith(fakeReadiness{}, 0)
}

func newFixtureWith(readiness httpapi.ReadinessChecker, requestTimeout time.Duration) *fixture {
	executor := &fakeExecutor{}
	lister := &fakeLister{}
	logs := &bytes.Buffer{}
	router := httpapi.NewRouter(httpapi.Dependencies{
		Executor:       executor,
		Lister:         lister,
		Readiness:      readiness,
		Logger:         logging.New(logs, slog.LevelInfo),
		RequestTimeout: requestTimeout,
	})
	return &fixture{executor: executor, lister: lister, logs: logs, router: router, requestTimeout: requestTimeout}
}

func (f *fixture) do(request *http.Request) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	f.router.ServeHTTP(recorder, request)
	return recorder
}

func (f *fixture) post(body string) *httptest.ResponseRecorder {
	return f.do(newPost(body))
}

func (f *fixture) get(target string) *httptest.ResponseRecorder {
	return f.do(httptest.NewRequest(http.MethodGet, target, nil))
}

func newPost(body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/transactions", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	return request
}

func pendingTransaction(t *testing.T) *domain.Transaction {
	t.Helper()
	transaction, err := domain.NewTransaction(domain.NewTransactionParams{
		ID:          fixedID,
		AccountID:   "acc-123456",
		Type:        domain.TransactionTypeCredit,
		Amount:      domain.NewMoneyFromCents(150000),
		Currency:    domain.CurrencyMXN,
		Description: "Transferencia recibida",
		CreatedAt:   fixedNow,
	})
	if err != nil {
		t.Fatalf("build transaction: %v", err)
	}
	return transaction
}

func executedTransaction(t *testing.T) *domain.Transaction {
	t.Helper()
	transaction := pendingTransaction(t)
	if err := transaction.MarkExecuted("txn-789", domain.NewMoneyFromCents(550000), fixedNow); err != nil {
		t.Fatalf("resolve transaction: %v", err)
	}
	return transaction
}

func rejectedTransaction(t *testing.T) *domain.Transaction {
	t.Helper()
	transaction := pendingTransaction(t)
	if err := transaction.MarkRejected("INSUFFICIENT_FUNDS", "Account balance is not enough", fixedNow); err != nil {
		t.Fatalf("resolve transaction: %v", err)
	}
	return transaction
}

func failedTransaction(t *testing.T, code string) *domain.Transaction {
	t.Helper()
	transaction := pendingTransaction(t)
	if err := transaction.MarkFailed(code, "the provider did not answer", fixedNow); err != nil {
		t.Fatalf("resolve transaction: %v", err)
	}
	return transaction
}

type problemBody struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Code      string `json:"code"`
	Detail    string `json:"detail"`
	RequestID string `json:"requestId"`
	Errors    []struct {
		Field   string `json:"field"`
		Message string `json:"message"`
	} `json:"errors"`
}

func decodeProblem(t *testing.T, recorder *httptest.ResponseRecorder) problemBody {
	t.Helper()
	if got := recorder.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", got)
	}
	var body problemBody
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("response %q is not a valid problem document: %v", recorder.Body.String(), err)
	}
	if body.Status != recorder.Code {
		t.Errorf("problem status = %d, want it to match the HTTP status %d", body.Status, recorder.Code)
	}
	if body.RequestID == "" {
		t.Error("problem document must carry the request id")
	}
	return body
}

func assertProblem(t *testing.T, recorder *httptest.ResponseRecorder, wantStatus int, wantCode string) problemBody {
	t.Helper()
	if recorder.Code != wantStatus {
		t.Fatalf("status = %d, want %d (body %s)", recorder.Code, wantStatus, recorder.Body.String())
	}
	body := decodeProblem(t, recorder)
	if body.Code != wantCode {
		t.Errorf("code = %q, want %q", body.Code, wantCode)
	}
	return body
}

func (p problemBody) fields() []string {
	fields := make([]string, 0, len(p.Errors))
	for _, fieldErr := range p.Errors {
		fields = append(fields, fieldErr.Field)
	}
	return fields
}

func decodeFields(t *testing.T, recorder *httptest.ResponseRecorder) map[string]json.RawMessage {
	t.Helper()
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &fields); err != nil {
		t.Fatalf("response %q is not valid JSON: %v", recorder.Body.String(), err)
	}
	return fields
}

func assertFields(t *testing.T, fields map[string]json.RawMessage, want map[string]string) {
	t.Helper()
	for name, wantValue := range want {
		if got := string(fields[name]); got != wantValue {
			t.Errorf("field %s = %s, want %s", name, got, wantValue)
		}
	}
}
