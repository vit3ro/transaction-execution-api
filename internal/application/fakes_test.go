package application_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vit3ro/transaction-execution-api/internal/domain"
	"github.com/vit3ro/transaction-execution-api/internal/ports"
)

var (
	fixedID  = uuid.MustParse("0192f0c1-7c3a-7b1e-8f00-000000000001")
	fixedNow = time.Date(2025, 3, 15, 10, 30, 0, 0, time.UTC)
)

type fakeRepository struct {
	createErr error
	updateErr error
	listErr   error

	created      []domain.Transaction
	updated      []domain.Transaction
	updateCtxErr error

	find      func(call int, key string) (*domain.Transaction, error)
	findCalls int

	listResult     []domain.Transaction
	listTotal      int64
	listCalls      int
	listFilter     ports.TransactionFilter
	listPagination ports.Pagination
}

func (r *fakeRepository) Create(_ context.Context, transaction *domain.Transaction) error {
	if r.createErr != nil {
		return r.createErr
	}
	r.created = append(r.created, *transaction)
	return nil
}

func (r *fakeRepository) Update(ctx context.Context, transaction *domain.Transaction) error {
	r.updateCtxErr = ctx.Err()
	if r.updateErr != nil {
		return r.updateErr
	}
	r.updated = append(r.updated, *transaction)
	return nil
}

func (r *fakeRepository) FindByIdempotencyKey(_ context.Context, key string) (*domain.Transaction, error) {
	r.findCalls++
	if r.find == nil {
		return nil, ports.ErrTransactionNotFound
	}
	return r.find(r.findCalls, key)
}

func (r *fakeRepository) List(_ context.Context, filter ports.TransactionFilter, pagination ports.Pagination) ([]domain.Transaction, int64, error) {
	r.listCalls++
	r.listFilter = filter
	r.listPagination = pagination
	if r.listErr != nil {
		return nil, 0, r.listErr
	}
	return r.listResult, r.listTotal, nil
}

type fakeProvider struct {
	result    ports.ProviderResult
	err       error
	onExecute func()

	requests []ports.ProviderRequest
}

func (p *fakeProvider) Execute(_ context.Context, request ports.ProviderRequest) (ports.ProviderResult, error) {
	p.requests = append(p.requests, request)
	if p.onExecute != nil {
		p.onExecute()
	}
	return p.result, p.err
}

type fixedClock struct {
	now time.Time
}

func (c fixedClock) Now() time.Time {
	return c.now
}

type fixedIDGenerator struct {
	id  uuid.UUID
	err error
}

func (g fixedIDGenerator) NewID() (uuid.UUID, error) {
	return g.id, g.err
}

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func mustParseMoney(t *testing.T, value string) domain.Money {
	t.Helper()
	money, err := domain.ParseMoney(value)
	if err != nil {
		t.Fatalf("invalid test amount %q: %v", value, err)
	}
	return money
}
