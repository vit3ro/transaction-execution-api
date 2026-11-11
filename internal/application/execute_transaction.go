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

var (
	ErrOutcomeNotPersisted  = errors.New("transaction outcome could not be persisted")
	ErrIdempotencyKeyReused = errors.New("idempotency key was already used with a different request")
)

var failureMessages = map[string]string{
	FailureCodeProviderTimeout:         "the provider did not respond in time",
	FailureCodeProviderUnavailable:     "the provider could not be reached",
	FailureCodeProviderInvalidResponse: "the provider returned a response outside the contract",
}

type ExecuteTransactionInput struct {
	AccountID   string
	Type        domain.TransactionType
	Amount      domain.Money
	Currency    domain.Currency
	Description string

	IdempotencyKey string
}

type ExecuteTransactionResult struct {
	Transaction *domain.Transaction
	Replayed    bool
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

func (uc *ExecuteTransaction) Execute(ctx context.Context, input ExecuteTransactionInput) (ExecuteTransactionResult, error) {
	transaction, err := uc.newPendingTransaction(input)
	if err != nil {
		return ExecuteTransactionResult{}, err
	}

	if previous, err := uc.previousExecution(ctx, input); err != nil || previous != nil {
		return ExecuteTransactionResult{Transaction: previous, Replayed: previous != nil}, err
	}

	if err := uc.repository.Create(ctx, transaction); err != nil {
		return uc.resolveCreateFailure(ctx, input, err)
	}

	result, providerErr := uc.provider.Execute(ctx, ports.ProviderRequest{
		AccountID: transaction.AccountID,
		Type:      transaction.Type,
		Amount:    transaction.Amount,
		Currency:  transaction.Currency,
	})

	if err := uc.applyOutcome(ctx, transaction, result, providerErr); err != nil {
		return ExecuteTransactionResult{}, err
	}
	if err := uc.persistOutcome(ctx, transaction); err != nil {
		return ExecuteTransactionResult{}, err
	}
	return ExecuteTransactionResult{Transaction: transaction}, nil
}

func (uc *ExecuteTransaction) previousExecution(ctx context.Context, input ExecuteTransactionInput) (*domain.Transaction, error) {
	if input.IdempotencyKey == "" {
		return nil, nil
	}

	previous, err := uc.repository.FindByIdempotencyKey(ctx, input.IdempotencyKey)
	if errors.Is(err, ports.ErrTransactionNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("look up idempotency key: %w", err)
	}
	if !isSameRequest(previous, input) {
		return nil, ErrIdempotencyKeyReused
	}
	return previous, nil
}

func (uc *ExecuteTransaction) resolveCreateFailure(ctx context.Context, input ExecuteTransactionInput, createErr error) (ExecuteTransactionResult, error) {
	if errors.Is(createErr, ports.ErrIdempotencyKeyConflict) {
		winner, err := uc.previousExecution(ctx, input)
		if err != nil {
			return ExecuteTransactionResult{}, err
		}
		if winner != nil {
			return ExecuteTransactionResult{Transaction: winner, Replayed: true}, nil
		}
	}
	return ExecuteTransactionResult{}, fmt.Errorf("persist pending transaction: %w", createErr)
}

func isSameRequest(previous *domain.Transaction, input ExecuteTransactionInput) bool {
	return previous.AccountID == input.AccountID &&
		previous.Type == input.Type &&
		previous.Amount == input.Amount &&
		previous.Currency == input.Currency &&
		previous.Description == input.Description
}

func (uc *ExecuteTransaction) newPendingTransaction(input ExecuteTransactionInput) (*domain.Transaction, error) {
	id, err := uc.ids.NewID()
	if err != nil {
		return nil, fmt.Errorf("generate transaction id: %w", err)
	}

	return domain.NewTransaction(domain.NewTransactionParams{
		ID:             id,
		AccountID:      input.AccountID,
		Type:           input.Type,
		Amount:         input.Amount,
		Currency:       input.Currency,
		Description:    input.Description,
		IdempotencyKey: input.IdempotencyKey,
		CreatedAt:      uc.clock.Now(),
	})
}

func (uc *ExecuteTransaction) applyOutcome(ctx context.Context, transaction *domain.Transaction, result ports.ProviderResult, providerErr error) error {
	now := uc.clock.Now()

	switch {
	case providerErr != nil:
		return uc.markFailed(ctx, transaction, failureCodeFor(providerErr), providerErr, now)
	case result.Status == ports.ProviderStatusApproved:
		return transaction.MarkExecuted(result.TransactionID, result.Balance, now)
	case result.Status == ports.ProviderStatusRejected:
		return transaction.MarkRejected(result.RejectionCode, result.RejectionMessage, now)
	default:
		cause := fmt.Errorf("unexpected provider status %q", result.Status)
		return uc.markFailed(ctx, transaction, FailureCodeProviderInvalidResponse, cause, now)
	}
}

func (uc *ExecuteTransaction) markFailed(ctx context.Context, transaction *domain.Transaction, code string, cause error, at time.Time) error {
	uc.logger.ErrorContext(ctx, "provider execution failed",
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
		uc.logger.ErrorContext(ctx, "transaction outcome not persisted, reconciliation required",
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
	switch {
	case errors.Is(providerErr, context.DeadlineExceeded):
		return FailureCodeProviderTimeout
	case errors.Is(providerErr, ports.ErrProviderInvalidResponse):
		return FailureCodeProviderInvalidResponse
	default:
		return FailureCodeProviderUnavailable
	}
}
