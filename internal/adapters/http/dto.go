package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/vit3ro/transaction-execution-api/internal/application"
	"github.com/vit3ro/transaction-execution-api/internal/domain"
)

const (
	maxRequestBodyBytes  = 64 << 10
	unknownFieldPrefix   = "json: unknown field "
	currencyCodeLength   = 3
	messageRequired      = "is required"
	messageInvalidAmount = "must be a number with at most two decimal places"
)

// Pointers tell a missing field apart from an empty one. The amount stays as
// raw JSON so it never becomes a float64.
type createTransactionRequest struct {
	AccountID   *string         `json:"accountId"`
	Type        *string         `json:"type"`
	Amount      json.RawMessage `json:"amount"`
	Currency    *string         `json:"currency"`
	Description *string         `json:"description"`
}

type transactionResponse struct {
	ID                    string       `json:"id"`
	AccountID             string       `json:"accountId"`
	Type                  string       `json:"type"`
	Amount                json.Number  `json:"amount"`
	Currency              string       `json:"currency"`
	Description           string       `json:"description"`
	Status                string       `json:"status"`
	ProviderTransactionID string       `json:"providerTransactionId,omitempty"`
	BalanceAfter          *json.Number `json:"balanceAfter,omitempty"`
	RejectionCode         string       `json:"rejectionCode,omitempty"`
	RejectionMessage      string       `json:"rejectionMessage,omitempty"`
	CreatedAt             string       `json:"createdAt"`
}

type transactionListResponse struct {
	Data  []transactionResponse `json:"data"`
	Page  int                   `json:"page"`
	Limit int                   `json:"limit"`
	Total int64                 `json:"total"`
}

type decodeFailure struct {
	status int
	code   string
	detail string
	fields []fieldError
}

func decodeCreateRequest(w http.ResponseWriter, r *http.Request) (createTransactionRequest, *decodeFailure) {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBodyBytes))
	decoder.DisallowUnknownFields()

	var request createTransactionRequest
	if err := decoder.Decode(&request); err != nil {
		return createTransactionRequest{}, classifyDecodeError(err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return createTransactionRequest{}, invalidJSON("request body must contain a single JSON object")
	}
	return request, nil
}

func classifyDecodeError(err error) *decodeFailure {
	var tooLarge *http.MaxBytesError
	var typeErr *json.UnmarshalTypeError

	switch {
	case errors.As(err, &tooLarge):
		return &decodeFailure{
			status: http.StatusRequestEntityTooLarge,
			code:   codeRequestTooLarge,
			detail: "request body is too large",
		}
	case errors.As(err, &typeErr) && typeErr.Field != "":
		return invalidFields(fieldError{Field: typeErr.Field, Message: "has an invalid type"})
	case errors.As(err, &typeErr):
		return invalidJSON("request body must be a JSON object")
	case strings.HasPrefix(err.Error(), unknownFieldPrefix):
		field := strings.Trim(strings.TrimPrefix(err.Error(), unknownFieldPrefix), `"`)
		return invalidFields(fieldError{Field: field, Message: "is not an accepted field"})
	default:
		return invalidJSON("request body must be valid JSON")
	}
}

func invalidJSON(detail string) *decodeFailure {
	return &decodeFailure{status: http.StatusBadRequest, code: codeInvalidJSON, detail: detail}
}

func invalidFields(fields ...fieldError) *decodeFailure {
	return &decodeFailure{
		status: http.StatusBadRequest,
		code:   codeValidationError,
		detail: "request has invalid fields",
		fields: fields,
	}
}

// Reports every invalid field in one response instead of one per request.
// Only the shape is checked here; business rules belong to the domain.
func (r createTransactionRequest) toInput() (application.ExecuteTransactionInput, []fieldError) {
	var fieldErrors []fieldError
	reject := func(field, message string) {
		fieldErrors = append(fieldErrors, fieldError{Field: field, Message: message})
	}

	var input application.ExecuteTransactionInput

	switch {
	case r.AccountID == nil:
		reject("accountId", messageRequired)
	case strings.TrimSpace(*r.AccountID) == "":
		reject("accountId", "must not be empty")
	default:
		input.AccountID = *r.AccountID
	}

	switch {
	case r.Type == nil:
		reject("type", messageRequired)
	case !domain.TransactionType(*r.Type).IsValid():
		reject("type", "must be CREDIT or DEBIT")
	default:
		input.Type = domain.TransactionType(*r.Type)
	}

	switch amount, err := parseAmount(r.Amount); {
	case isAbsent(r.Amount):
		reject("amount", messageRequired)
	case err != nil:
		reject("amount", messageInvalidAmount)
	default:
		input.Amount = amount
	}

	switch {
	case r.Currency == nil:
		reject("currency", messageRequired)
	case !isCurrencyCode(*r.Currency):
		reject("currency", "must be a three-letter uppercase ISO 4217 code")
	default:
		input.Currency = domain.Currency(*r.Currency)
	}

	if r.Description != nil {
		input.Description = *r.Description
	}
	return input, fieldErrors
}

func isAbsent(raw json.RawMessage) bool {
	return len(raw) == 0 || bytes.Equal(raw, []byte("null"))
}

func parseAmount(raw json.RawMessage) (domain.Money, error) {
	return domain.ParseMoney(string(raw))
}

func isCurrencyCode(value string) bool {
	if len(value) != currencyCodeLength {
		return false
	}
	for _, char := range value {
		if char < 'A' || char > 'Z' {
			return false
		}
	}
	return true
}

func toTransactionResponse(transaction *domain.Transaction) transactionResponse {
	response := transactionResponse{
		ID:                    transaction.ID.String(),
		AccountID:             transaction.AccountID,
		Type:                  string(transaction.Type),
		Amount:                json.Number(transaction.Amount.String()),
		Currency:              string(transaction.Currency),
		Description:           transaction.Description,
		Status:                string(transaction.Status),
		ProviderTransactionID: transaction.ProviderTransactionID,
		RejectionCode:         transaction.RejectionCode,
		RejectionMessage:      transaction.RejectionMessage,
		// RFC 3339 in UTC without fractions, as in the challenge example.
		CreatedAt: transaction.CreatedAt.UTC().Format(time.RFC3339),
	}
	if transaction.BalanceAfter != nil {
		balance := json.Number(transaction.BalanceAfter.String())
		response.BalanceAfter = &balance
	}
	return response
}

func toTransactionListResponse(page application.TransactionPage) transactionListResponse {
	data := make([]transactionResponse, 0, len(page.Transactions))
	for index := range page.Transactions {
		data = append(data, toTransactionResponse(&page.Transactions[index]))
	}
	return transactionListResponse{
		Data:  data,
		Page:  page.Page,
		Limit: page.Limit,
		Total: page.Total,
	}
}
