package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/vit3ro/transaction-execution-api/internal/domain"
	"github.com/vit3ro/transaction-execution-api/internal/ports"
)

const (
	DefaultPage  = 1
	DefaultLimit = 20
	MaxLimit     = 100
)

var (
	ErrInvalidPage         = errors.New("page must be greater than or equal to 1")
	ErrInvalidLimit        = fmt.Errorf("limit must be between 1 and %d", MaxLimit)
	ErrInvalidStatusFilter = errors.New("status must be PENDING, EXECUTED, REJECTED or FAILED")
)

type ListTransactionsInput struct {
	AccountID string
	Status    domain.TransactionStatus
	Type      domain.TransactionType
	Page      *int
	Limit     *int
}

type TransactionPage struct {
	Transactions []domain.Transaction
	Total        int64
	Page         int
	Limit        int
}

type ListTransactions struct {
	repository ports.TransactionRepository
}

func NewListTransactions(repository ports.TransactionRepository) *ListTransactions {
	return &ListTransactions{repository: repository}
}

func (uc *ListTransactions) Execute(ctx context.Context, input ListTransactionsInput) (TransactionPage, error) {
	filter, err := buildFilter(input)
	if err != nil {
		return TransactionPage{}, err
	}
	pagination, err := buildPagination(input)
	if err != nil {
		return TransactionPage{}, err
	}

	transactions, total, err := uc.repository.List(ctx, filter, pagination)
	if err != nil {
		return TransactionPage{}, fmt.Errorf("list transactions: %w", err)
	}

	return TransactionPage{
		Transactions: transactions,
		Total:        total,
		Page:         pagination.Page,
		Limit:        pagination.Limit,
	}, nil
}

func buildFilter(input ListTransactionsInput) (ports.TransactionFilter, error) {
	if input.Status != "" && !input.Status.IsValid() {
		return ports.TransactionFilter{}, &domain.ValidationError{Field: "status", Rule: ErrInvalidStatusFilter}
	}
	if input.Type != "" && !input.Type.IsValid() {
		return ports.TransactionFilter{}, &domain.ValidationError{Field: "type", Rule: domain.ErrInvalidTransactionType}
	}
	return ports.TransactionFilter{
		AccountID: input.AccountID,
		Status:    input.Status,
		Type:      input.Type,
	}, nil
}

func buildPagination(input ListTransactionsInput) (ports.Pagination, error) {
	pagination := ports.Pagination{Page: DefaultPage, Limit: DefaultLimit}

	if input.Page != nil {
		if *input.Page < 1 {
			return ports.Pagination{}, &domain.ValidationError{Field: "page", Rule: ErrInvalidPage}
		}
		pagination.Page = *input.Page
	}
	if input.Limit != nil {
		if *input.Limit < 1 || *input.Limit > MaxLimit {
			return ports.Pagination{}, &domain.ValidationError{Field: "limit", Rule: ErrInvalidLimit}
		}
		pagination.Limit = *input.Limit
	}
	return pagination, nil
}
