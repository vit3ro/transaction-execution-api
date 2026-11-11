package postgres

import (
	"fmt"
	"strings"

	"github.com/vit3ro/transaction-execution-api/internal/ports"
)

// Amounts travel as text in both directions (::text here, ::numeric on
// insert) so they never go through a float. NULLs are read as empty strings.
const transactionColumns = `id, account_id, type, amount::text, currency, description, status,
	COALESCE(provider_transaction_id, ''), balance_after::text,
	COALESCE(rejection_code, ''), COALESCE(rejection_message, ''),
	COALESCE(idempotency_key, ''), created_at, updated_at`

const insertTransactionSQL = `
INSERT INTO transactions (
	id, account_id, type, amount, currency, description, status,
	provider_transaction_id, balance_after, rejection_code, rejection_message,
	idempotency_key, created_at, updated_at
) VALUES (
	$1, $2, $3, $4::numeric, $5, $6, $7,
	NULLIF($8::text, ''), $9::numeric, NULLIF($10::text, ''), NULLIF($11::text, ''),
	NULLIF($12::text, ''), $13, $14
)`

const findByIdempotencyKeySQL = "SELECT " + transactionColumns + " FROM transactions WHERE idempotency_key = $1"

const idempotencyKeyIndex = "idx_transactions_idempotency_key"

const updateTransactionSQL = `
UPDATE transactions SET
	status = $2,
	provider_transaction_id = NULLIF($3::text, ''),
	balance_after = $4::numeric,
	rejection_code = NULLIF($5::text, ''),
	rejection_message = NULLIF($6::text, ''),
	updated_at = $7
WHERE id = $1`

// id breaks ties between rows created in the same microsecond, so pages are
// stable. The indexes end in the same two columns.
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

// Column names come from our own code; user values always go as parameters.
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

// When a page is not full the total is already known (offset + rows), so we
// skip a count(*) that is expensive on a large table.
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
