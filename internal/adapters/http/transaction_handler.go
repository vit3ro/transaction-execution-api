package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"

	"github.com/vit3ro/transaction-execution-api/internal/application"
	"github.com/vit3ro/transaction-execution-api/internal/domain"
)

const (
	headerIdempotencyKey     = "Idempotency-Key"
	headerIdempotentReplayed = "Idempotent-Replayed"
)

// A malformed request is a 400. These three are valid requests that break a
// business rule, so they are a 422 with a stable code.
var businessRuleCodes = map[error]string{
	domain.ErrAmountBelowMinimum:      codeAmountBelowMinimum,
	domain.ErrDebitAmountExceedsLimit: codeDebitAmountExceedsLimit,
	domain.ErrUnsupportedCurrency:     codeUnsupportedCurrency,
}

var failureStatusCodes = map[string]int{
	application.FailureCodeProviderTimeout:         http.StatusGatewayTimeout,
	application.FailureCodeProviderInvalidResponse: http.StatusBadGateway,
	application.FailureCodeProviderUnavailable:     http.StatusServiceUnavailable,
}

type TransactionExecutor interface {
	Execute(ctx context.Context, input application.ExecuteTransactionInput) (application.ExecuteTransactionResult, error)
}

type TransactionLister interface {
	Execute(ctx context.Context, input application.ListTransactionsInput) (application.TransactionPage, error)
}

type transactionHandler struct {
	executor TransactionExecutor
	lister   TransactionLister
	logger   *slog.Logger
}

func (h *transactionHandler) create(w http.ResponseWriter, r *http.Request) {
	request, failure := decodeCreateRequest(w, r)
	if failure != nil {
		writeProblem(w, r, failure.status, failure.code, failure.detail, failure.fields...)
		return
	}

	input, fieldErrors := request.toInput()
	if len(fieldErrors) > 0 {
		writeProblem(w, r, http.StatusBadRequest, codeValidationError, "request has invalid fields", fieldErrors...)
		return
	}
	input.IdempotencyKey = r.Header.Get(headerIdempotencyKey)

	result, err := h.executor.Execute(r.Context(), input)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeExecution(w, r, result)
}

func (h *transactionHandler) writeExecution(w http.ResponseWriter, r *http.Request, result application.ExecuteTransactionResult) {
	if result.Replayed {
		w.Header().Set(headerIdempotentReplayed, "true")
	}
	if result.Transaction.Status == domain.TransactionStatusPending {
		writeProblem(w, r, http.StatusConflict, codeRequestInProgress,
			"a request with this idempotency key is still being processed")
		return
	}
	writeJSON(w, executionStatusCode(result), toTransactionResponse(result.Transaction))
}

// A provider rejection is still a 201: the transaction was created and
// recorded, and the client reads the result in status. A technical failure
// maps to a gateway error but still returns the transaction.
func executionStatusCode(result application.ExecuteTransactionResult) int {
	if result.Transaction.Status == domain.TransactionStatusFailed {
		if status, known := failureStatusCodes[result.Transaction.RejectionCode]; known {
			return status
		}
		return http.StatusServiceUnavailable
	}
	if result.Replayed {
		return http.StatusOK
	}
	return http.StatusCreated
}

func (h *transactionHandler) list(w http.ResponseWriter, r *http.Request) {
	input, fieldErrors := parseListQuery(r.URL.Query())
	if len(fieldErrors) > 0 {
		writeProblem(w, r, http.StatusBadRequest, codeValidationError, "request has invalid query parameters", fieldErrors...)
		return
	}

	page, err := h.lister.Execute(r.Context(), input)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toTransactionListResponse(page))
}

func parseListQuery(query url.Values) (application.ListTransactionsInput, []fieldError) {
	input := application.ListTransactionsInput{
		AccountID: query.Get("accountId"),
		Status:    domain.TransactionStatus(query.Get("status")),
		Type:      domain.TransactionType(query.Get("type")),
	}

	var fieldErrors []fieldError
	input.Page, fieldErrors = parseIntParam(query, "page", fieldErrors)
	input.Limit, fieldErrors = parseIntParam(query, "limit", fieldErrors)
	return input, fieldErrors
}

func parseIntParam(query url.Values, name string, fieldErrors []fieldError) (*int, []fieldError) {
	if !query.Has(name) {
		return nil, fieldErrors
	}
	value, err := strconv.Atoi(query.Get(name))
	if err != nil {
		return nil, append(fieldErrors, fieldError{Field: name, Message: "must be an integer"})
	}
	return &value, fieldErrors
}

func (h *transactionHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var validationErr *domain.ValidationError

	switch {
	case errors.As(err, &validationErr):
		status, code := classifyValidationError(validationErr)
		writeProblem(w, r, status, code, validationErr.Error(),
			fieldError{Field: validationErr.Field, Message: validationErr.Error()})
	case errors.Is(err, application.ErrIdempotencyKeyReused):
		writeProblem(w, r, http.StatusUnprocessableEntity, codeIdempotencyKeyReused, application.ErrIdempotencyKeyReused.Error())
	case errors.Is(err, context.DeadlineExceeded):
		h.logFailure(r, slog.LevelWarn, "request timed out", err)
		writeProblem(w, r, http.StatusGatewayTimeout, codeRequestTimeout, "the request took too long to complete")
	default:
		// Unknown errors get a generic message; the detail stays in the log.
		h.logFailure(r, slog.LevelError, "request failed", err)
		writeProblem(w, r, http.StatusInternalServerError, codeInternalError, "an unexpected error occurred")
	}
}

func classifyValidationError(err *domain.ValidationError) (status int, code string) {
	for rule, ruleCode := range businessRuleCodes {
		if errors.Is(err, rule) {
			return http.StatusUnprocessableEntity, ruleCode
		}
	}
	return http.StatusBadRequest, codeValidationError
}

func (h *transactionHandler) logFailure(r *http.Request, level slog.Level, message string, err error) {
	h.logger.Log(r.Context(), level, message,
		"method", r.Method,
		"path", r.URL.Path,
		"error", err,
	)
}
