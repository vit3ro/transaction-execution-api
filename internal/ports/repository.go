package ports

import (
	"context"

	"github.com/vit3ro/transaction-execution-api/internal/domain"
)

type TransactionFilter struct {
	AccountID string
	Status    domain.TransactionStatus
	Type      domain.TransactionType
}

type Pagination struct {
	Page  int
	Limit int
}

func (p Pagination) Offset() int {
	return (p.Page - 1) * p.Limit
}

type TransactionRepository interface {
	Create(ctx context.Context, transaction *domain.Transaction) error
	Update(ctx context.Context, transaction *domain.Transaction) error
	FindByIdempotencyKey(ctx context.Context, key string) (*domain.Transaction, error)
	List(ctx context.Context, filter TransactionFilter, pagination Pagination) (transactions []domain.Transaction, total int64, err error)
}
