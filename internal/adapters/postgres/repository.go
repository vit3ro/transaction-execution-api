package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vit3ro/transaction-execution-api/internal/domain"
	"github.com/vit3ro/transaction-execution-api/internal/ports"
)

const uniqueViolationCode = "23505"

var _ ports.TransactionRepository = (*Repository)(nil)

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) Create(ctx context.Context, transaction *domain.Transaction) error {
	_, err := r.pool.Exec(ctx, insertTransactionSQL,
		transaction.ID,
		transaction.AccountID,
		string(transaction.Type),
		transaction.Amount.String(),
		string(transaction.Currency),
		transaction.Description,
		string(transaction.Status),
		transaction.ProviderTransactionID,
		balanceAfterParam(transaction),
		transaction.RejectionCode,
		transaction.RejectionMessage,
		transaction.CreatedAt,
		transaction.UpdatedAt,
	)
	if isUniqueViolation(err) {
		return fmt.Errorf("%w: %s", ports.ErrTransactionAlreadyExists, transaction.ID)
	}
	if err != nil {
		return fmt.Errorf("insert transaction: %w", err)
	}
	return nil
}

func (r *Repository) Update(ctx context.Context, transaction *domain.Transaction) error {
	tag, err := r.pool.Exec(ctx, updateTransactionSQL,
		transaction.ID,
		string(transaction.Status),
		transaction.ProviderTransactionID,
		balanceAfterParam(transaction),
		transaction.RejectionCode,
		transaction.RejectionMessage,
		transaction.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("update transaction: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s", ports.ErrTransactionNotFound, transaction.ID)
	}
	return nil
}

func (r *Repository) List(ctx context.Context, filter ports.TransactionFilter, pagination ports.Pagination) ([]domain.Transaction, int64, error) {
	where := newWhereClause(filter)

	transactions, err := r.selectPage(ctx, where, pagination)
	if err != nil {
		return nil, 0, err
	}

	if total, known := deduceTotal(len(transactions), pagination); known {
		return transactions, total, nil
	}
	total, err := r.count(ctx, where)
	if err != nil {
		return nil, 0, err
	}
	return transactions, total, nil
}

func (r *Repository) selectPage(ctx context.Context, where whereClause, pagination ports.Pagination) ([]domain.Transaction, error) {
	sql, args := pageQuery(where, pagination)

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("select transactions: %w", err)
	}
	transactions, err := pgx.CollectRows(rows, scanTransaction)
	if err != nil {
		return nil, fmt.Errorf("read transactions: %w", err)
	}
	return transactions, nil
}

func (r *Repository) count(ctx context.Context, where whereClause) (int64, error) {
	sql, args := countQuery(where)

	var total int64
	if err := r.pool.QueryRow(ctx, sql, args...).Scan(&total); err != nil {
		return 0, fmt.Errorf("count transactions: %w", err)
	}
	return total, nil
}

func scanTransaction(row pgx.CollectableRow) (domain.Transaction, error) {
	var (
		transaction  domain.Transaction
		txType       string
		amount       string
		currency     string
		status       string
		balanceAfter *string
	)
	err := row.Scan(
		&transaction.ID,
		&transaction.AccountID,
		&txType,
		&amount,
		&currency,
		&transaction.Description,
		&status,
		&transaction.ProviderTransactionID,
		&balanceAfter,
		&transaction.RejectionCode,
		&transaction.RejectionMessage,
		&transaction.CreatedAt,
		&transaction.UpdatedAt,
	)
	if err != nil {
		return domain.Transaction{}, err
	}

	transaction.Type = domain.TransactionType(txType)
	transaction.Currency = domain.Currency(currency)
	transaction.Status = domain.TransactionStatus(status)
	transaction.CreatedAt = transaction.CreatedAt.UTC()
	transaction.UpdatedAt = transaction.UpdatedAt.UTC()

	if transaction.Amount, err = domain.ParseMoney(amount); err != nil {
		return domain.Transaction{}, fmt.Errorf("stored amount: %w", err)
	}
	if balanceAfter != nil {
		balance, err := domain.ParseMoney(*balanceAfter)
		if err != nil {
			return domain.Transaction{}, fmt.Errorf("stored balance: %w", err)
		}
		transaction.BalanceAfter = &balance
	}
	return transaction, nil
}

func balanceAfterParam(transaction *domain.Transaction) *string {
	if transaction.BalanceAfter == nil {
		return nil
	}
	balance := transaction.BalanceAfter.String()
	return &balance
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolationCode
}
