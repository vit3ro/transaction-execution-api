package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/vit3ro/transaction-execution-api/internal/logging"
)

const (
	contentTypeJSON    = "application/json"
	contentTypeProblem = "application/problem+json"
	problemTypeBlank   = "about:blank"
)

const (
	codeInvalidJSON             = "INVALID_JSON"
	codeValidationError         = "VALIDATION_ERROR"
	codeRequestTooLarge         = "REQUEST_TOO_LARGE"
	codeAmountBelowMinimum      = "AMOUNT_BELOW_MINIMUM"
	codeDebitAmountExceedsLimit = "DEBIT_AMOUNT_EXCEEDS_LIMIT"
	codeUnsupportedCurrency     = "UNSUPPORTED_CURRENCY"
	codeIdempotencyKeyReused    = "IDEMPOTENCY_KEY_REUSED"
	codeRequestInProgress       = "IDEMPOTENT_REQUEST_IN_PROGRESS"
	codeRequestTimeout          = "REQUEST_TIMEOUT"
	codeInternalError           = "INTERNAL_ERROR"
)

type fieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

type problem struct {
	Type      string       `json:"type"`
	Title     string       `json:"title"`
	Status    int          `json:"status"`
	Code      string       `json:"code"`
	Detail    string       `json:"detail"`
	Errors    []fieldError `json:"errors,omitempty"`
	RequestID string       `json:"requestId,omitempty"`
}

func writeProblem(w http.ResponseWriter, r *http.Request, status int, code, detail string, fieldErrors ...fieldError) {
	w.Header().Set("Content-Type", contentTypeProblem)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem{
		Type:      problemTypeBlank,
		Title:     http.StatusText(status),
		Status:    status,
		Code:      code,
		Detail:    detail,
		Errors:    fieldErrors,
		RequestID: logging.RequestID(r.Context()),
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", contentTypeJSON)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
