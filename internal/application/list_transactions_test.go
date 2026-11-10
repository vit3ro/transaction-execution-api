package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/vit3ro/transaction-execution-api/internal/application"
	"github.com/vit3ro/transaction-execution-api/internal/domain"
	"github.com/vit3ro/transaction-execution-api/internal/ports"
)

func intPtr(value int) *int {
	return &value
}

func TestListTransactions_AppliesDefaultPagination(t *testing.T) {
	repository := &fakeRepository{}
	useCase := application.NewListTransactions(repository)

	page, err := useCase.Execute(context.Background(), application.ListTransactionsInput{})
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}

	want := ports.Pagination{Page: application.DefaultPage, Limit: application.DefaultLimit}
	if repository.listPagination != want {
		t.Errorf("pagination = %+v, want %+v", repository.listPagination, want)
	}
	if repository.listFilter != (ports.TransactionFilter{}) {
		t.Errorf("filter = %+v, want no filters", repository.listFilter)
	}
	if page.Page != application.DefaultPage || page.Limit != application.DefaultLimit {
		t.Errorf("page = %d and limit = %d, want the defaults", page.Page, page.Limit)
	}
}

func TestListTransactions_PassesFiltersAndReturnsPage(t *testing.T) {
	stored := []domain.Transaction{
		{ID: fixedID, AccountID: "acc-123456", Status: domain.TransactionStatusExecuted},
	}
	repository := &fakeRepository{listResult: stored, listTotal: 41}
	useCase := application.NewListTransactions(repository)

	page, err := useCase.Execute(context.Background(), application.ListTransactionsInput{
		AccountID: "acc-123456",
		Status:    domain.TransactionStatusExecuted,
		Type:      domain.TransactionTypeDebit,
		Page:      intPtr(3),
		Limit:     intPtr(20),
	})
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}

	wantFilter := ports.TransactionFilter{
		AccountID: "acc-123456",
		Status:    domain.TransactionStatusExecuted,
		Type:      domain.TransactionTypeDebit,
	}
	if repository.listFilter != wantFilter {
		t.Errorf("filter = %+v, want %+v", repository.listFilter, wantFilter)
	}
	wantPagination := ports.Pagination{Page: 3, Limit: 20}
	if repository.listPagination != wantPagination {
		t.Errorf("pagination = %+v, want %+v", repository.listPagination, wantPagination)
	}
	if repository.listPagination.Offset() != 40 {
		t.Errorf("offset = %d, want 40", repository.listPagination.Offset())
	}
	if len(page.Transactions) != 1 || page.Transactions[0].ID != fixedID {
		t.Errorf("transactions = %+v, want the stored transaction", page.Transactions)
	}
	if page.Total != 41 || page.Page != 3 || page.Limit != 20 {
		t.Errorf("page metadata = (total %d, page %d, limit %d), want (41, 3, 20)", page.Total, page.Page, page.Limit)
	}
}

func TestListTransactions_AcceptsLimitBoundaries(t *testing.T) {
	for _, limit := range []int{1, application.MaxLimit} {
		repository := &fakeRepository{}
		useCase := application.NewListTransactions(repository)

		_, err := useCase.Execute(context.Background(), application.ListTransactionsInput{Limit: intPtr(limit)})
		if err != nil {
			t.Errorf("limit %d returned unexpected error: %v", limit, err)
		}
		if repository.listPagination.Limit != limit {
			t.Errorf("limit = %d, want %d", repository.listPagination.Limit, limit)
		}
	}
}

func TestListTransactions_RejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name      string
		input     application.ListTransactionsInput
		wantErr   error
		wantField string
	}{
		{"page zero", application.ListTransactionsInput{Page: intPtr(0)}, application.ErrInvalidPage, "page"},
		{"negative page", application.ListTransactionsInput{Page: intPtr(-1)}, application.ErrInvalidPage, "page"},
		{"limit zero", application.ListTransactionsInput{Limit: intPtr(0)}, application.ErrInvalidLimit, "limit"},
		{"negative limit", application.ListTransactionsInput{Limit: intPtr(-10)}, application.ErrInvalidLimit, "limit"},
		{"limit over the maximum", application.ListTransactionsInput{Limit: intPtr(application.MaxLimit + 1)}, application.ErrInvalidLimit, "limit"},
		{"unknown status", application.ListTransactionsInput{Status: "APPROVED"}, application.ErrInvalidStatusFilter, "status"},
		{"unknown type", application.ListTransactionsInput{Type: "TRANSFER"}, domain.ErrInvalidTransactionType, "type"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repository := &fakeRepository{}
			useCase := application.NewListTransactions(repository)

			_, err := useCase.Execute(context.Background(), tt.input)

			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Execute error = %v, want %v", err, tt.wantErr)
			}
			var validationErr *domain.ValidationError
			if !errors.As(err, &validationErr) || validationErr.Field != tt.wantField {
				t.Errorf("error %v should be a ValidationError on field %q", err, tt.wantField)
			}
			if repository.listCalls != 0 {
				t.Error("the repository must not be queried with invalid input")
			}
		})
	}
}

func TestListTransactions_PropagatesRepositoryError(t *testing.T) {
	storageErr := errors.New("query timeout")
	repository := &fakeRepository{listErr: storageErr}
	useCase := application.NewListTransactions(repository)

	_, err := useCase.Execute(context.Background(), application.ListTransactionsInput{})

	if !errors.Is(err, storageErr) {
		t.Fatalf("Execute error = %v, want it to wrap %v", err, storageErr)
	}
}
