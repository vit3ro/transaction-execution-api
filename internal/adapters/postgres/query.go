package postgres

import (
	"fmt"
	"strings"

	"github.com/vit3ro/transaction-execution-api/internal/ports"
)

const transactionColumns = `id, account_id, type, amount::text, currency, description, status,
	COALESCE(provider_transaction_id, ''), balance_after::text,
	COALESCE(rejection_code, ''), COALESCE(rejection_message, ''),
	created_at, updated_at`

const insertTransactionSQL = `
INSERT INTO transactions (
	id, account_id, type, amount, currency, description, status,
	provider_transaction_id, balance_after, rejection_code, rejection_message,
	created_at, updated_at
) VALUES (
	$1, $2, $3, $4::numeric, $5, $6, $7,
	NULLIF($8::text, ''), $9::numeric, NULLIF($10::text, ''), NULLIF($11::text, ''),
	$12, $13
)`

const updateTransactionSQL = `
UPDATE transactions SET
	status = $2,
	provider_transaction_id = NULLIF($3::text, ''),
	balance_after = $4::numeric,
	rejection_code = NULLIF($5::text, ''),
	rejection_message = NULLIF($6::text, ''),
	updated_at = $7
WHERE id = $1`

const listOrder = " ORDER BY created_at DESC, id DESC"

type whereClause struct {
	conditions []string
	args       []any
}

func newWhereClause(filter ports.TransactionFilter) whereClause {
	var where whereClause
	if filter.AccountID != "" {
		where.add("account_id", filter.AccountID)
	}
	if filter.Status != "" {
		where.add("status", string(filter.Status))
	}
	if filter.Type != "" {
		where.add("type", string(filter.Type))
	}
	return where
}

func (w *whereClause) add(column string, value any) {
	w.args = append(w.args, value)
	w.conditions = append(w.conditions, fmt.Sprintf("%s = $%d", column, len(w.args)))
}

func (w whereClause) sql() string {
	if len(w.conditions) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(w.conditions, " AND ")
}

func pageQuery(where whereClause, pagination ports.Pagination) (string, []any) {
	args := append(append([]any{}, where.args...), pagination.Limit, pagination.Offset())
	sql := fmt.Sprintf("SELECT %s FROM transactions%s%s LIMIT $%d OFFSET $%d",
		transactionColumns, where.sql(), listOrder, len(args)-1, len(args))
	return sql, args
}

func countQuery(where whereClause) (string, []any) {
	return "SELECT count(*) FROM transactions" + where.sql(), where.args
}

func deduceTotal(rows int, pagination ports.Pagination) (total int64, known bool) {
	offset := pagination.Offset()
	if rows == 0 && offset > 0 {
		return 0, false
	}
	if rows >= pagination.Limit {
		return 0, false
	}
	return int64(offset + rows), true
}
