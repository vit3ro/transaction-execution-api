package httpapi_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/vit3ro/transaction-execution-api/internal/application"
	"github.com/vit3ro/transaction-execution-api/internal/domain"
)

type listBody struct {
	Data  []map[string]json.RawMessage `json:"data"`
	Page  int                          `json:"page"`
	Limit int                          `json:"limit"`
	Total int64                        `json:"total"`
}

func decodeList(t *testing.T, body []byte) listBody {
	t.Helper()
	var list listBody
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("response %q is not a valid list: %v", body, err)
	}
	return list
}

func TestListTransactions_ReturnsPage(t *testing.T) {
	f := newFixture()
	f.lister.page = application.TransactionPage{
		Transactions: []domain.Transaction{*executedTransaction(t), *rejectedTransaction(t)},
		Total:        42,
		Page:         2,
		Limit:        2,
	}

	recorder := f.get("/transactions?accountId=acc-123456&status=EXECUTED&type=CREDIT&page=2&limit=2")

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	list := decodeList(t, recorder.Body.Bytes())
	if list.Page != 2 || list.Limit != 2 || list.Total != 42 {
		t.Errorf("pagination = (page %d, limit %d, total %d), want (2, 2, 42)", list.Page, list.Limit, list.Total)
	}
	if len(list.Data) != 2 {
		t.Fatalf("data has %d items, want 2", len(list.Data))
	}
	assertFields(t, list.Data[0], map[string]string{
		"status":       `"EXECUTED"`,
		"amount":       `1500.00`,
		"balanceAfter": `5500.00`,
		"createdAt":    `"2025-03-15T10:30:00Z"`,
	})
	assertFields(t, list.Data[1], map[string]string{
		"status":        `"REJECTED"`,
		"rejectionCode": `"INSUFFICIENT_FUNDS"`,
	})

	if len(f.lister.inputs) != 1 {
		t.Fatalf("use case calls = %d, want 1", len(f.lister.inputs))
	}
	input := f.lister.inputs[0]
	if input.AccountID != "acc-123456" || input.Status != domain.TransactionStatusExecuted || input.Type != domain.TransactionTypeCredit {
		t.Errorf("filters = %+v, want account acc-123456, status EXECUTED and type CREDIT", input)
	}
	if input.Page == nil || *input.Page != 2 || input.Limit == nil || *input.Limit != 2 {
		t.Errorf("pagination input = (%v, %v), want (2, 2)", input.Page, input.Limit)
	}
}

func TestListTransactions_WithoutParametersLeavesDefaultsToTheUseCase(t *testing.T) {
	f := newFixture()
	f.lister.page = application.TransactionPage{Page: 1, Limit: 20}

	recorder := f.get("/transactions")

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	input := f.lister.inputs[0]
	if input.Page != nil || input.Limit != nil {
		t.Errorf("pagination input = (%v, %v), want both unset", input.Page, input.Limit)
	}
	if input.AccountID != "" || input.Status != "" || input.Type != "" {
		t.Errorf("filters = %+v, want none", input)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &raw); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if string(raw["data"]) != "[]" {
		t.Errorf("data = %s, want an empty array", raw["data"])
	}
}

func TestListTransactions_NonNumericPagination(t *testing.T) {
	tests := []struct {
		target     string
		wantFields []string
	}{
		{"/transactions?page=abc", []string{"page"}},
		{"/transactions?limit=ten", []string{"limit"}},
		{"/transactions?page=1.5&limit=", []string{"page", "limit"}},
	}

	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			f := newFixture()

			recorder := f.get(tt.target)

			problem := assertProblem(t, recorder, http.StatusBadRequest, "VALIDATION_ERROR")
			if !slices.Equal(problem.fields(), tt.wantFields) {
				t.Errorf("fields = %v, want %v", problem.fields(), tt.wantFields)
			}
			if len(f.lister.inputs) != 0 {
				t.Error("the use case must not be called with non numeric pagination")
			}
		})
	}
}

func TestListTransactions_UseCaseValidationErrors(t *testing.T) {
	rules := map[string]error{
		"page":   application.ErrInvalidPage,
		"limit":  application.ErrInvalidLimit,
		"status": application.ErrInvalidStatusFilter,
		"type":   domain.ErrInvalidTransactionType,
	}

	for field, rule := range rules {
		t.Run(field, func(t *testing.T) {
			f := newFixture()
			f.lister.err = &domain.ValidationError{Field: field, Rule: rule}

			recorder := f.get("/transactions?page=0")

			problem := assertProblem(t, recorder, http.StatusBadRequest, "VALIDATION_ERROR")
			if problem.Detail != rule.Error() {
				t.Errorf("detail = %q, want %q", problem.Detail, rule.Error())
			}
			if !slices.Equal(problem.fields(), []string{field}) {
				t.Errorf("fields = %v, want [%s]", problem.fields(), field)
			}
		})
	}
}

func TestListTransactions_UnexpectedError(t *testing.T) {
	f := newFixture()
	f.lister.err = errors.New("relation transactions does not exist")

	recorder := f.get("/transactions")

	problem := assertProblem(t, recorder, http.StatusInternalServerError, "INTERNAL_ERROR")
	if problem.Detail != "an unexpected error occurred" {
		t.Errorf("detail = %q, want a generic message", problem.Detail)
	}
}

func TestRouter_UnknownPathIsProblemDetails(t *testing.T) {
	for _, target := range []string{"/unknown", "/transactions/123", "/"} {
		t.Run(target, func(t *testing.T) {
			f := newFixture()

			recorder := f.get(target)

			assertProblem(t, recorder, http.StatusNotFound, "NOT_FOUND")
		})
	}
}

func TestRouter_UnsupportedMethodIsProblemDetails(t *testing.T) {
	tests := []struct {
		method    string
		target    string
		wantAllow []string
	}{
		{http.MethodDelete, "/transactions", []string{"GET", "HEAD", "POST"}},
		{http.MethodPut, "/transactions", []string{"GET", "HEAD", "POST"}},
		{http.MethodPost, "/healthz", []string{"GET", "HEAD"}},
	}

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.target, func(t *testing.T) {
			f := newFixture()

			recorder := f.do(newRequest(tt.method, tt.target))

			assertProblem(t, recorder, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")
			allow := strings.Split(strings.ReplaceAll(recorder.Header().Get("Allow"), " ", ""), ",")
			slices.Sort(allow)
			if !slices.Equal(allow, tt.wantAllow) {
				t.Errorf("Allow = %v, want %v", allow, tt.wantAllow)
			}
			if len(f.executor.inputs) != 0 || len(f.lister.inputs) != 0 {
				t.Error("no use case must run for an unsupported method")
			}
		})
	}
}

func TestRouter_MatchedRoutesAreNotRewritten(t *testing.T) {
	f := newFixture()

	recorder := f.do(newRequest(http.MethodHead, "/healthz"))

	if recorder.Code != http.StatusOK {
		t.Errorf("HEAD /healthz status = %d, want 200 from the GET route", recorder.Code)
	}
}
