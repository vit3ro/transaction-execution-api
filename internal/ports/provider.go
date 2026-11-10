package ports

import (
	"context"
	"time"

	"github.com/vit3ro/transaction-execution-api/internal/domain"
)

type ProviderStatus string

const (
	ProviderStatusApproved ProviderStatus = "APPROVED"
	ProviderStatusRejected ProviderStatus = "REJECTED"
)

type ProviderRequest struct {
	AccountID string
	Type      domain.TransactionType
	Amount    domain.Money
	Currency  domain.Currency
}

type ProviderResult struct {
	Status           ProviderStatus
	TransactionID    string
	Balance          domain.Money
	ExecutedAt       time.Time
	RejectionCode    string
	RejectionMessage string
}

type ProviderClient interface {
	Execute(ctx context.Context, request ProviderRequest) (ProviderResult, error)
}
