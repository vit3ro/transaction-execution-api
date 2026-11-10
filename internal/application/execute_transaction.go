package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/vit3ro/transaction-execution-api/internal/domain"
	"github.com/vit3ro/transaction-execution-api/internal/ports"
)

const (
	FailureCodeProviderTimeout         = "PROVIDER_TIMEOUT"
	FailureCodeProviderUnavailable     = "PROVIDER_UNAVAILABLE"
	FailureCodeProviderInvalidResponse = "PROVIDER_INVALID_RESPONSE"
)

const outcomePersistTimeout = 5 * time.Second

var ErrOutcomeNotPersisted = errors.New("transaction outcome could not be persisted")

var failureMessages = map[string]string{
	FailureCodeProviderTimeout:         "the provider did not respond in time",
	FailureCodeProviderUnavailable:     "the provider could not be reached or returned an invalid response",
	FailureCodeProviderInvalidResponse: "the provider returned an unexpected status",
}

type ExecuteTransactionInput struct {
	AccountID   string
	Type        domain.TransactionType
	Amount      domain.Money
	Currency    domain.Currency
	Description string
}

type ExecuteTransaction struct {
	repository ports.TransactionRepository
	provider   ports.ProviderClient
	clock      ports.Clock
	ids        ports.IDGenerator
	logger     *slog.Logger
}

func NewExecuteTransaction(
	repository ports.TransactionRepository,
	provider ports.ProviderClient,
	clock ports.Clock,
	ids ports.IDGenerator,
	logger *slog.Logger,
) *ExecuteTransaction {
	return &ExecuteTransaction{
		repository: repository,
		provider:   provider,
		clock:      clock,
		ids:        ids,
		logger:     logger,
	}
}

func (uc *ExecuteTransaction) Execute(ctx context.Context, input ExecuteTransactionInput) (*domain.Transaction, error) {
	transaction, err := uc.newPendingTransaction(input)
	if err != nil {
		return nil, err
	}

	if err := uc.repository.Create(ctx, transaction); err != nil {
		return nil, fmt.Errorf("persist pending transaction: %w", err)
	}

	result, providerErr := uc.provider.Execute(ctx, ports.ProviderRequest{
		AccountID: transaction.AccountID,
		Type:      transaction.Type,
		Amount:    transaction.Amount,
		Currency:  transaction.Currency,
	})

	if err := uc.applyOutcome(transaction, result, providerErr); err != nil {
		return nil, err
	}
	if err := uc.persistOutcome(ctx, transaction); err != nil {
		return nil, err
	}
	return transaction, nil
}

func (uc *ExecuteTransaction) newPendingTransaction(input ExecuteTransactionInput) (*domain.Transaction, error) {
	id, err := uc.ids.NewID()
	if err != nil {
		return nil, fmt.Errorf("generate transaction id: %w", err)
	}

	return domain.NewTransaction(domain.NewTransactionParams{
		ID:          id,
		AccountID:   input.AccountID,
		Type:        input.Type,
		Amount:      input.Amount,
		Currency:    input.Currency,
		Description: input.Description,
		CreatedAt:   uc.clock.Now(),
	})
}

func (uc *ExecuteTransaction) applyOutcome(transaction *domain.Transaction, result ports.ProviderResult, providerErr error) error {
	now := uc.clock.Now()

	switch {
	case providerErr != nil:
		return uc.markFailed(transaction, failureCodeFor(providerErr), providerErr, now)
	case result.Status == ports.ProviderStatusApproved:
		return transaction.MarkExecuted(result.TransactionID, result.Balance, now)
	case result.Status == ports.ProviderStatusRejected:
		return transaction.MarkRejected(result.RejectionCode, result.RejectionMessage, now)
	default:
		cause := fmt.Errorf("unexpected provider status %q", result.Status)
		return uc.markFailed(transaction, FailureCodeProviderInvalidResponse, cause, now)
	}
}

func (uc *ExecuteTransaction) markFailed(transaction *domain.Transaction, code string, cause error, at time.Time) error {
	uc.logger.Error("provider execution failed",
		"transactionId", transaction.ID,
		"failureCode", code,
		"error", cause,
	)
	return transaction.MarkFailed(code, failureMessages[code], at)
}

func (uc *ExecuteTransaction) persistOutcome(ctx context.Context, transaction *domain.Transaction) error {
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), outcomePersistTimeout)
	defer cancel()

	if err := uc.repository.Update(persistCtx, transaction); err != nil {
		uc.logger.Error("transaction outcome not persisted, reconciliation required",
			"transactionId", transaction.ID,
			"status", transaction.Status,
			"providerTransactionId", transaction.ProviderTransactionID,
			"error", err,
		)
		return fmt.Errorf("%w: %w", ErrOutcomeNotPersisted, err)
	}
	return nil
}

func failureCodeFor(providerErr error) string {
	if errors.Is(providerErr, context.DeadlineExceeded) {
		return FailureCodeProviderTimeout
	}
	return FailureCodeProviderUnavailable
}
