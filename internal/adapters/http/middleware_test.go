package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vit3ro/transaction-execution-api/internal/application"
)

func newRequest(method, target string) *http.Request {
	return httptest.NewRequest(method, target, nil)
}

type logEntry struct {
	Level      string `json:"level"`
	Message    string `json:"msg"`
	RequestID  string `json:"requestId"`
	Method     string `json:"method"`
	Path       string `json:"path"`
	Status     int    `json:"status"`
	DurationMs *int64 `json:"durationMs"`
}

func accessLog(t *testing.T, f *fixture) logEntry {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(f.logs.String()), "\n") {
		var entry logEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("log line %q is not JSON: %v", line, err)
		}
		if entry.Message == "http request" {
			return entry
		}
	}
	t.Fatalf("no access log entry found in %q", f.logs.String())
	return logEntry{}
}

func TestRequestID_IsGeneratedWhenMissing(t *testing.T) {
	f := newFixture()

	recorder := f.get("/healthz")

	requestID := recorder.Header().Get("X-Request-ID")
	if _, err := uuid.Parse(requestID); err != nil {
		t.Errorf("X-Request-ID = %q, want a generated UUID", requestID)
	}
}

func TestRequestID_IsPropagatedWhenProvided(t *testing.T) {
	f := newFixture()
	request := newPost(`{`)
	request.Header.Set("X-Request-ID", "req-abc-123")

	recorder := f.do(request)

	if got := recorder.Header().Get("X-Request-ID"); got != "req-abc-123" {
		t.Errorf("X-Request-ID = %q, want req-abc-123", got)
	}
	if problem := decodeProblem(t, recorder); problem.RequestID != "req-abc-123" {
		t.Errorf("problem requestId = %q, want req-abc-123", problem.RequestID)
	}
	if entry := accessLog(t, f); entry.RequestID != "req-abc-123" {
		t.Errorf("logged requestId = %q, want req-abc-123", entry.RequestID)
	}
}

func TestRequestID_OversizedValueIsReplaced(t *testing.T) {
	f := newFixture()
	request := newRequest(http.MethodGet, "/healthz")
	request.Header.Set("X-Request-ID", strings.Repeat("x", 500))

	recorder := f.do(request)

	if _, err := uuid.Parse(recorder.Header().Get("X-Request-ID")); err != nil {
		t.Errorf("X-Request-ID = %q, want a generated UUID", recorder.Header().Get("X-Request-ID"))
	}
}

func TestLogging_RecordsStatusAndDuration(t *testing.T) {
	f := newFixture()
	f.executor.result = application.ExecuteTransactionResult{Transaction: executedTransaction(t)}

	f.post(validBody)

	entry := accessLog(t, f)
	if entry.Method != http.MethodPost || entry.Path != "/transactions" || entry.Status != http.StatusCreated {
		t.Errorf("access log = %+v, want POST /transactions with status 201", entry)
	}
	if entry.DurationMs == nil {
		t.Error("access log must include the request duration")
	}
	if entry.Level != "INFO" {
		t.Errorf("level = %q, want INFO", entry.Level)
	}
	if strings.Contains(f.logs.String(), "acc-123456") {
		t.Error("the access log must not include request body data")
	}
}

func TestLogging_ServerErrorsUseErrorLevel(t *testing.T) {
	f := newFixture()
	f.executor.err = errors.New("boom")

	f.post(validBody)

	if entry := accessLog(t, f); entry.Level != "ERROR" || entry.Status != http.StatusInternalServerError {
		t.Errorf("access log = %+v, want level ERROR and status 500", entry)
	}
}

type panickingLister struct{}

func (panickingLister) Execute(context.Context, application.ListTransactionsInput) (application.TransactionPage, error) {
	panic("nil pointer somewhere deep")
}

func TestRecovery_TurnsPanicsIntoInternalErrors(t *testing.T) {
	f := newFixture()
	router := newRouterWithLister(f, panickingLister{})
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, newRequest(http.MethodGet, "/transactions"))

	problem := assertProblem(t, recorder, http.StatusInternalServerError, "INTERNAL_ERROR")
	if strings.Contains(recorder.Body.String(), "nil pointer") {
		t.Errorf("response leaks the panic value: %s", recorder.Body.String())
	}
	if problem.Detail != "an unexpected error occurred" {
		t.Errorf("detail = %q, want a generic message", problem.Detail)
	}
	if !strings.Contains(f.logs.String(), "panic recovered") || !strings.Contains(f.logs.String(), "nil pointer somewhere deep") {
		t.Error("the panic must be written to the log")
	}
}

type deadlineLister struct {
	hasDeadline bool
	remaining   time.Duration
}

func (l *deadlineLister) Execute(ctx context.Context, _ application.ListTransactionsInput) (application.TransactionPage, error) {
	deadline, ok := ctx.Deadline()
	l.hasDeadline = ok
	l.remaining = time.Until(deadline)
	<-ctx.Done()
	return application.TransactionPage{}, ctx.Err()
}

func TestTimeout_AppliesDeadlineToTheRequestContext(t *testing.T) {
	f := newFixtureWith(fakeReadiness{}, 30*time.Millisecond)
	lister := &deadlineLister{}
	router := newRouterWithLister(f, lister)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, newRequest(http.MethodGet, "/transactions"))

	if !lister.hasDeadline || lister.remaining > 30*time.Millisecond {
		t.Errorf("deadline = (%t, %v), want a deadline within 30ms", lister.hasDeadline, lister.remaining)
	}
	assertProblem(t, recorder, http.StatusGatewayTimeout, "REQUEST_TIMEOUT")
}

func TestHealthz_IsAlwaysOK(t *testing.T) {
	f := newFixtureWith(fakeReadiness{err: errors.New("database down")}, 0)

	recorder := f.get("/healthz")

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	assertFields(t, decodeFields(t, recorder), map[string]string{"status": `"ok"`})
}

func TestReadyz_ReflectsDatabaseState(t *testing.T) {
	tests := []struct {
		name       string
		pingErr    error
		wantStatus int
		wantBody   string
	}{
		{"database reachable", nil, http.StatusOK, `"ready"`},
		{"database unreachable", errors.New("dial tcp 10.0.0.5:5432: connection refused"), http.StatusServiceUnavailable, `"unavailable"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixtureWith(fakeReadiness{err: tt.pingErr}, 0)

			recorder := f.get("/readyz")

			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, tt.wantStatus)
			}
			assertFields(t, decodeFields(t, recorder), map[string]string{"status": tt.wantBody})
			if strings.Contains(recorder.Body.String(), "10.0.0.5") {
				t.Errorf("readiness response leaks infrastructure details: %s", recorder.Body.String())
			}
		})
	}
}
