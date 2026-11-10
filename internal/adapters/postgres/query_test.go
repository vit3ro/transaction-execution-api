package postgres

import (
	"reflect"
	"strings"
	"testing"

	"github.com/vit3ro/transaction-execution-api/internal/domain"
	"github.com/vit3ro/transaction-execution-api/internal/ports"
)

func TestWhereClause(t *testing.T) {
	tests := []struct {
		name     string
		filter   ports.TransactionFilter
		wantSQL  string
		wantArgs []any
	}{
		{
			name:     "no filters",
			filter:   ports.TransactionFilter{},
			wantSQL:  "",
			wantArgs: nil,
		},
		{
			name:     "account only",
			filter:   ports.TransactionFilter{AccountID: "acc-1"},
			wantSQL:  " WHERE account_id = $1",
			wantArgs: []any{"acc-1"},
		},
		{
			name:     "status only",
			filter:   ports.TransactionFilter{Status: domain.TransactionStatusFailed},
			wantSQL:  " WHERE status = $1",
			wantArgs: []any{"FAILED"},
		},
		{
			name:     "type only",
			filter:   ports.TransactionFilter{Type: domain.TransactionTypeDebit},
			wantSQL:  " WHERE type = $1",
			wantArgs: []any{"DEBIT"},
		},
		{
			name: "all filters",
			filter: ports.TransactionFilter{
				AccountID: "acc-1",
				Status:    domain.TransactionStatusExecuted,
				Type:      domain.TransactionTypeCredit,
			},
			wantSQL:  " WHERE account_id = $1 AND status = $2 AND type = $3",
			wantArgs: []any{"acc-1", "EXECUTED", "CREDIT"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			where := newWhereClause(tt.filter)

			if where.sql() != tt.wantSQL {
				t.Errorf("sql = %q, want %q", where.sql(), tt.wantSQL)
			}
			if !reflect.DeepEqual(where.args, tt.wantArgs) {
				t.Errorf("args = %v, want %v", where.args, tt.wantArgs)
			}
		})
	}
}

func TestListQueries_NeverEmbedUserInput(t *testing.T) {
	malicious := "acc-1'; DROP TABLE transactions; --"
	where := newWhereClause(ports.TransactionFilter{AccountID: malicious})

	pageSQL, pageArgs := pageQuery(where, ports.Pagination{Page: 3, Limit: 20})
	countSQL, countArgs := countQuery(where)

	for name, sql := range map[string]string{"page": pageSQL, "count": countSQL} {
		if strings.Contains(sql, "DROP") || strings.Contains(sql, malicious) {
			t.Errorf("%s query embeds user input: %s", name, sql)
		}
	}
	if !reflect.DeepEqual(pageArgs, []any{malicious, 20, 40}) {
		t.Errorf("page args = %v, want the account id, limit 20 and offset 40", pageArgs)
	}
	if !reflect.DeepEqual(countArgs, []any{malicious}) {
		t.Errorf("count args = %v, want only the account id", countArgs)
	}
	if !strings.HasSuffix(pageSQL, " ORDER BY created_at DESC, id DESC LIMIT $2 OFFSET $3") {
		t.Errorf("page query = %q, want deterministic order and parameterized pagination", pageSQL)
	}
}

func TestDeduceTotal(t *testing.T) {
	tests := []struct {
		name       string
		rows       int
		pagination ports.Pagination
		wantTotal  int64
		wantKnown  bool
	}{
		{"empty first page", 0, ports.Pagination{Page: 1, Limit: 20}, 0, true},
		{"partial first page", 7, ports.Pagination{Page: 1, Limit: 20}, 7, true},
		{"partial later page", 5, ports.Pagination{Page: 3, Limit: 20}, 45, true},
		{"full page needs a count", 20, ports.Pagination{Page: 1, Limit: 20}, 0, false},
		{"empty later page needs a count", 0, ports.Pagination{Page: 4, Limit: 20}, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			total, known := deduceTotal(tt.rows, tt.pagination)

			if known != tt.wantKnown || total != tt.wantTotal {
				t.Errorf("deduceTotal = (%d, %t), want (%d, %t)", total, known, tt.wantTotal, tt.wantKnown)
			}
		})
	}
}
