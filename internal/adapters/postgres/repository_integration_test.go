//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vit3ro/transaction-execution-api/internal/adapters/postgres"
	"github.com/vit3ro/transaction-execution-api/internal/domain"
	"github.com/vit3ro/transaction-execution-api/internal/ports"
)

const (
	databaseURLEnv = "TEST_DATABASE_URL"
	migrationsDir  = "../../../migrations"
)

var (
	testPool *pgxpool.Pool
	baseTime = time.Date(2025, 3, 15, 10, 30, 0, 0, time.UTC)
)

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	databaseURL := os.Getenv(databaseURLEnv)
	if databaseURL == "" {
		fmt.Fprintf(os.Stderr, "%s must point to a disposable PostgreSQL database\n", databaseURLEnv)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := postgres.NewPool(ctx, postgres.Config{DatabaseURL: databaseURL, MaxConns: 5})
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect to test database: %v\n", err)
		return 1
	}
	defer pool.Close()

	for _, migration := range []string{"000001_create_transactions.down.sql", "000001_create_transactions.up.sql"} {
		if err := applyMigration(ctx, pool, migration); err != nil {
			fmt.Fprintf(os.Stderr, "apply %s: %v\n", migration, err)
			return 1
		}
	}

	testPool = pool
	return m.Run()
}

func applyMigration(ctx context.Context, pool *pgxpool.Pool, name string) error {
	statements, err := os.ReadFile(filepath.Join(migrationsDir, name))
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, string(statements), pgx.QueryExecModeSimpleProtocol)
	return err
}

func newRepository(t *testing.T) *postgres.Repository {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), "TRUNCATE TABLE transactions"); err != nil {
		t.Fatalf("truncate transactions: %v", err)
	}
	return postgres.NewRepository(testPool)
}

type transactionOption func(*domain.NewTransactionParams)

func withAccount(accountID string) transactionOption {
	return func(p *domain.NewTransactionParams) { p.AccountID = accountID }
}

func withType(txType domain.TransactionType) transactionOption {
	return func(p *domain.NewTransactionParams) { p.Type = txType }
}

func withCreatedAt(createdAt time.Time) transactionOption {
	return func(p *domain.NewTransactionParams) { p.CreatedAt = createdAt }
}

func withID(id uuid.UUID) transactionOption {
	return func(p *domain.NewTransactionParams) { p.ID = id }
}

func newTransaction(t *testing.T, options ...transactionOption) *domain.Transaction {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("generate id: %v", err)
	}
	params := domain.NewTransactionParams{
		ID:          id,
		AccountID:   "acc-123456",
		Type:        domain.TransactionTypeCredit,
		Amount:      domain.NewMoneyFromCents(150000),
		Currency:    domain.CurrencyMXN,
		Description: "Transferencia recibida",
		CreatedAt:   baseTime,
	}
	for _, option := range options {
		option(&params)
	}
	transaction, err := domain.NewTransaction(params)
	if err != nil {
		t.Fatalf("build transaction: %v", err)
	}
	return transaction
}

func mustCreate(t *testing.T, repository *postgres.Repository, transaction *domain.Transaction) {
	t.Helper()
	if err := repository.Create(context.Background(), transaction); err != nil {
		t.Fatalf("Create returned unexpected error: %v", err)
	}
}

func mustList(t *testing.T, repository *postgres.Repository, filter ports.TransactionFilter, pagination ports.Pagination) ([]domain.Transaction, int64) {
	t.Helper()
	transactions, total, err := repository.List(context.Background(), filter, pagination)
	if err != nil {
		t.Fatalf("List returned unexpected error: %v", err)
	}
	return transactions, total
}

func assertSameTransaction(t *testing.T, got domain.Transaction, want *domain.Transaction) {
	t.Helper()
	if got.ID != want.ID ||
		got.AccountID != want.AccountID ||
		got.Type != want.Type ||
		got.Amount != want.Amount ||
		got.Currency != want.Currency ||
		got.Description != want.Description ||
		got.Status != want.Status ||
		got.ProviderTransactionID != want.ProviderTransactionID ||
		got.RejectionCode != want.RejectionCode ||
		got.RejectionMessage != want.RejectionMessage ||
		!got.CreatedAt.Equal(want.CreatedAt) ||
		!got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Errorf("stored transaction = %+v, want %+v", got, *want)
	}
	switch {
	case got.BalanceAfter == nil && want.BalanceAfter == nil:
	case got.BalanceAfter == nil || want.BalanceAfter == nil || *got.BalanceAfter != *want.BalanceAfter:
		t.Errorf("stored balance = %v, want %v", got.BalanceAfter, want.BalanceAfter)
	}
}

func firstPage() ports.Pagination {
	return ports.Pagination{Page: 1, Limit: 20}
}

func TestRepository_CreateStoresPendingTransaction(t *testing.T) {
	repository := newRepository(t)
	transaction := newTransaction(t)

	mustCreate(t, repository, transaction)

	stored, total := mustList(t, repository, ports.TransactionFilter{}, firstPage())
	if total != 1 || len(stored) != 1 {
		t.Fatalf("stored %d transactions with total %d, want 1 and 1", len(stored), total)
	}
	assertSameTransaction(t, stored[0], transaction)
	if stored[0].CreatedAt.Location() != time.UTC {
		t.Errorf("CreatedAt location = %v, want UTC", stored[0].CreatedAt.Location())
	}
}

func TestRepository_CreateRejectsDuplicateID(t *testing.T) {
	repository := newRepository(t)
	transaction := newTransaction(t)
	mustCreate(t, repository, transaction)

	err := repository.Create(context.Background(), transaction)

	if !errors.Is(err, ports.ErrTransactionAlreadyExists) {
		t.Fatalf("Create error = %v, want ErrTransactionAlreadyExists", err)
	}
}

func TestRepository_UpdateStoresOutcome(t *testing.T) {
	resolvedAt := baseTime.Add(2 * time.Second)
	outcomes := map[string]func(*domain.Transaction) error{
		"executed": func(tx *domain.Transaction) error {
			return tx.MarkExecuted("txn-789", domain.NewMoneyFromCents(550000), resolvedAt)
		},
		"executed with zero balance": func(tx *domain.Transaction) error {
			return tx.MarkExecuted("txn-790", domain.NewMoneyFromCents(0), resolvedAt)
		},
		"rejected": func(tx *domain.Transaction) error {
			return tx.MarkRejected("INSUFFICIENT_FUNDS", "Account balance is not enough", resolvedAt)
		},
		"failed": func(tx *domain.Transaction) error {
			return tx.MarkFailed("PROVIDER_TIMEOUT", "the provider did not respond in time", resolvedAt)
		},
	}

	for name, resolve := range outcomes {
		t.Run(name, func(t *testing.T) {
			repository := newRepository(t)
			transaction := newTransaction(t)
			mustCreate(t, repository, transaction)
			if err := resolve(transaction); err != nil {
				t.Fatalf("resolve transaction: %v", err)
			}

			if err := repository.Update(context.Background(), transaction); err != nil {
				t.Fatalf("Update returned unexpected error: %v", err)
			}

			stored, _ := mustList(t, repository, ports.TransactionFilter{}, firstPage())
			if len(stored) != 1 {
				t.Fatalf("stored %d transactions, want 1", len(stored))
			}
			assertSameTransaction(t, stored[0], transaction)
		})
	}
}

func TestRepository_UpdateUnknownTransaction(t *testing.T) {
	repository := newRepository(t)
	transaction := newTransaction(t)

	err := repository.Update(context.Background(), transaction)

	if !errors.Is(err, ports.ErrTransactionNotFound) {
		t.Fatalf("Update error = %v, want ErrTransactionNotFound", err)
	}
}

func TestRepository_ListFilters(t *testing.T) {
	repository := newRepository(t)
	resolvedAt := baseTime.Add(time.Minute)

	creditA := newTransaction(t, withAccount("acc-A"), withCreatedAt(baseTime))
	debitA := newTransaction(t, withAccount("acc-A"), withType(domain.TransactionTypeDebit), withCreatedAt(baseTime.Add(time.Second)))
	creditB := newTransaction(t, withAccount("acc-B"), withCreatedAt(baseTime.Add(2*time.Second)))
	for _, transaction := range []*domain.Transaction{creditA, debitA, creditB} {
		mustCreate(t, repository, transaction)
	}
	if err := debitA.MarkRejected("INSUFFICIENT_FUNDS", "Account balance is not enough", resolvedAt); err != nil {
		t.Fatalf("resolve transaction: %v", err)
	}
	if err := repository.Update(context.Background(), debitA); err != nil {
		t.Fatalf("Update returned unexpected error: %v", err)
	}

	tests := []struct {
		name    string
		filter  ports.TransactionFilter
		wantIDs []uuid.UUID
	}{
		{"no filter", ports.TransactionFilter{}, []uuid.UUID{creditB.ID, debitA.ID, creditA.ID}},
		{"by account", ports.TransactionFilter{AccountID: "acc-A"}, []uuid.UUID{debitA.ID, creditA.ID}},
		{"by status", ports.TransactionFilter{Status: domain.TransactionStatusPending}, []uuid.UUID{creditB.ID, creditA.ID}},
		{"by type", ports.TransactionFilter{Type: domain.TransactionTypeCredit}, []uuid.UUID{creditB.ID, creditA.ID}},
		{
			"by account, status and type",
			ports.TransactionFilter{AccountID: "acc-A", Status: domain.TransactionStatusRejected, Type: domain.TransactionTypeDebit},
			[]uuid.UUID{debitA.ID},
		},
		{"no match", ports.TransactionFilter{AccountID: "acc-unknown"}, []uuid.UUID{}},
		{"input with SQL metacharacters", ports.TransactionFilter{AccountID: "acc-A' OR '1'='1"}, []uuid.UUID{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transactions, total := mustList(t, repository, tt.filter, firstPage())

			if total != int64(len(tt.wantIDs)) {
				t.Errorf("total = %d, want %d", total, len(tt.wantIDs))
			}
			if transactions == nil {
				t.Error("List must return an empty slice instead of nil")
			}
			if len(transactions) != len(tt.wantIDs) {
				t.Fatalf("returned %d transactions, want %d", len(transactions), len(tt.wantIDs))
			}
			for position, wantID := range tt.wantIDs {
				if transactions[position].ID != wantID {
					t.Errorf("position %d = %s, want %s", position, transactions[position].ID, wantID)
				}
			}
		})
	}
}

func TestRepository_ListOrdersTiesByIDDescending(t *testing.T) {
	repository := newRepository(t)
	lowID := uuid.MustParse("0192f0c1-0000-7000-8000-000000000001")
	midID := uuid.MustParse("0192f0c1-0000-7000-8000-000000000002")
	highID := uuid.MustParse("0192f0c1-0000-7000-8000-000000000003")
	for _, id := range []uuid.UUID{midID, highID, lowID} {
		mustCreate(t, repository, newTransaction(t, withID(id)))
	}

	transactions, _ := mustList(t, repository, ports.TransactionFilter{}, firstPage())

	wantOrder := []uuid.UUID{highID, midID, lowID}
	if len(transactions) != len(wantOrder) {
		t.Fatalf("returned %d transactions, want %d", len(transactions), len(wantOrder))
	}
	for position, wantID := range wantOrder {
		if transactions[position].ID != wantID {
			t.Errorf("position %d = %s, want %s", position, transactions[position].ID, wantID)
		}
	}
}

func TestRepository_ListPagination(t *testing.T) {
	repository := newRepository(t)
	const totalTransactions = 5
	created := make([]*domain.Transaction, 0, totalTransactions)
	for index := range totalTransactions {
		transaction := newTransaction(t, withCreatedAt(baseTime.Add(time.Duration(index)*time.Second)))
		mustCreate(t, repository, transaction)
		created = append(created, transaction)
	}

	tests := []struct {
		name       string
		pagination ports.Pagination
		wantIDs    []uuid.UUID
	}{
		{"first full page", ports.Pagination{Page: 1, Limit: 2}, []uuid.UUID{created[4].ID, created[3].ID}},
		{"second full page", ports.Pagination{Page: 2, Limit: 2}, []uuid.UUID{created[2].ID, created[1].ID}},
		{"last partial page", ports.Pagination{Page: 3, Limit: 2}, []uuid.UUID{created[0].ID}},
		{"page beyond the end", ports.Pagination{Page: 4, Limit: 2}, []uuid.UUID{}},
		{"page exactly the size of the table", ports.Pagination{Page: 1, Limit: 5}, []uuid.UUID{created[4].ID, created[3].ID, created[2].ID, created[1].ID, created[0].ID}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transactions, total := mustList(t, repository, ports.TransactionFilter{}, tt.pagination)

			if total != totalTransactions {
				t.Errorf("total = %d, want %d", total, totalTransactions)
			}
			if len(transactions) != len(tt.wantIDs) {
				t.Fatalf("returned %d transactions, want %d", len(transactions), len(tt.wantIDs))
			}
			for position, wantID := range tt.wantIDs {
				if transactions[position].ID != wantID {
					t.Errorf("position %d = %s, want %s", position, transactions[position].ID, wantID)
				}
			}
		})
	}
}

func TestRepository_SchemaRejectsInvalidRows(t *testing.T) {
	newRepository(t)
	const insert = `
		INSERT INTO transactions (id, account_id, type, amount, currency, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4::numeric, 'MXN', $5, now(), now())`

	invalidRows := map[string][]any{
		"unknown type":    {uuid.New(), "acc-1", "TRANSFER", "100.00", "PENDING"},
		"unknown status":  {uuid.New(), "acc-1", "CREDIT", "100.00", "APPROVED"},
		"zero amount":     {uuid.New(), "acc-1", "CREDIT", "0.00", "PENDING"},
		"negative amount": {uuid.New(), "acc-1", "CREDIT", "-5.00", "PENDING"},
		"empty account":   {uuid.New(), "", "CREDIT", "100.00", "PENDING"},
	}

	for name, args := range invalidRows {
		t.Run(name, func(t *testing.T) {
			if _, err := testPool.Exec(context.Background(), insert, args...); err == nil {
				t.Error("the schema accepted an invalid row")
			}
		})
	}
}

func TestRepository_HonoursContextCancellation(t *testing.T) {
	repository := newRepository(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := repository.Create(ctx, newTransaction(t)); !errors.Is(err, context.Canceled) {
		t.Errorf("Create error = %v, want context.Canceled", err)
	}
	if _, _, err := repository.List(ctx, ports.TransactionFilter{}, firstPage()); !errors.Is(err, context.Canceled) {
		t.Errorf("List error = %v, want context.Canceled", err)
	}
}
