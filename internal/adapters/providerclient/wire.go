package providerclient

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/vit3ro/transaction-execution-api/internal/domain"
	"github.com/vit3ro/transaction-execution-api/internal/ports"
)

const (
	wireStatusApproved = "APPROVED"
	wireStatusRejected = "REJECTED"
)

type executeRequest struct {
	AccountID string      `json:"accountId"`
	Type      string      `json:"type"`
	Amount    json.Number `json:"amount"`
	Currency  string      `json:"currency"`
}

type executeResponse struct {
	TransactionID string      `json:"transactionId"`
	Status        string      `json:"status"`
	Balance       json.Number `json:"balance"`
	ExecutedAt    time.Time   `json:"executedAt"`
	Code          string      `json:"code"`
	Message       string      `json:"message"`
}

func encodeRequest(request ports.ProviderRequest) ([]byte, error) {
	return json.Marshal(executeRequest{
		AccountID: request.AccountID,
		Type:      string(request.Type),
		Amount:    json.Number(request.Amount.String()),
		Currency:  string(request.Currency),
	})
}

func decodeResponse(statusCode int, body []byte) (ports.ProviderResult, error) {
	var response executeResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return ports.ProviderResult{}, invalidResponse(statusCode, err.Error())
	}

	switch {
	case statusCode == http.StatusOK:
		return toApprovedResult(statusCode, response)
	case statusCode >= http.StatusBadRequest:
		return toRejectedResult(statusCode, response)
	default:
		return ports.ProviderResult{}, invalidResponse(statusCode, "unexpected status code")
	}
}

func toApprovedResult(statusCode int, response executeResponse) (ports.ProviderResult, error) {
	if response.Status != wireStatusApproved {
		return ports.ProviderResult{}, invalidResponse(statusCode, fmt.Sprintf("unexpected status %q", response.Status))
	}
	if response.TransactionID == "" {
		return ports.ProviderResult{}, invalidResponse(statusCode, "missing transactionId")
	}
	balance, err := domain.ParseMoney(response.Balance.String())
	if err != nil {
		return ports.ProviderResult{}, invalidResponse(statusCode, fmt.Sprintf("invalid balance %q", response.Balance))
	}

	return ports.ProviderResult{
		Status:        ports.ProviderStatusApproved,
		TransactionID: response.TransactionID,
		Balance:       balance,
		ExecutedAt:    response.ExecutedAt,
	}, nil
}

func toRejectedResult(statusCode int, response executeResponse) (ports.ProviderResult, error) {
	if response.Status != wireStatusRejected {
		return ports.ProviderResult{}, invalidResponse(statusCode, fmt.Sprintf("unexpected status %q", response.Status))
	}
	if response.Code == "" {
		return ports.ProviderResult{}, invalidResponse(statusCode, "missing rejection code")
	}

	return ports.ProviderResult{
		Status:           ports.ProviderStatusRejected,
		RejectionCode:    response.Code,
		RejectionMessage: response.Message,
	}, nil
}

func invalidResponse(statusCode int, detail string) error {
	return fmt.Errorf("%w: http status %d: %s", ErrInvalidResponse, statusCode, detail)
}
