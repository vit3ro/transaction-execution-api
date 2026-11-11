package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

const (
	executePath         = "POST /provider/v1/execute"
	maxRequestBodyBytes = 64 << 10

	typeCredit = "CREDIT"
	typeDebit  = "DEBIT"

	statusApproved = "APPROVED"
	statusRejected = "REJECTED"

	accountInsufficient = "acc-insufficient"
	accountBlocked      = "acc-blocked"
	accountError        = "acc-error"
	accountDown         = "acc-down"
	accountGarbage      = "acc-garbage"
	accountSlow         = "acc-slow"
)

type executeRequest struct {
	AccountID string      `json:"accountId"`
	Type      string      `json:"type"`
	Amount    json.Number `json:"amount"`
	Currency  string      `json:"currency"`
}

type approvedResponse struct {
	TransactionID string      `json:"transactionId"`
	Status        string      `json:"status"`
	Balance       json.Number `json:"balance"`
	ExecutedAt    string      `json:"executedAt"`
}

type rejectedResponse struct {
	Status  string `json:"status"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type outcome struct {
	scenario    string
	statusCode  int
	contentType string
	body        []byte
}

type provider struct {
	initialBalance int64
	slowDelay      time.Duration
	logger         *slog.Logger
	now            func() time.Time

	mu       sync.Mutex
	balances map[string]int64
	sequence atomic.Uint64
}

func newProvider(initialBalance int64, slowDelay time.Duration, logger *slog.Logger) *provider {
	return &provider{
		initialBalance: initialBalance,
		slowDelay:      slowDelay,
		logger:         logger,
		now:            time.Now,
		balances:       make(map[string]int64),
	}
}

func (p *provider) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(executePath, p.execute)
	return mux
}

func (p *provider) execute(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	request, amount, err := decodeRequest(w, r)
	result := rejected("invalid-request", http.StatusBadRequest, "INVALID_REQUEST", errorMessage(err))
	if err == nil {
		result = p.resolve(r.Context(), request, amount)
	}

	w.Header().Set("Content-Type", result.contentType)
	w.WriteHeader(result.statusCode)
	_, _ = w.Write(result.body)

	p.logger.Info("provider request",
		"accountId", request.AccountID,
		"type", request.Type,
		"amount", request.Amount.String(),
		"scenario", result.scenario,
		"status", result.statusCode,
		"durationMs", time.Since(start).Milliseconds(),
	)
}

func (p *provider) resolve(ctx context.Context, request executeRequest, amount int64) outcome {
	switch request.AccountID {
	case accountInsufficient:
		return rejected("insufficient-funds", http.StatusUnprocessableEntity, "INSUFFICIENT_FUNDS", "Account balance is not enough")
	case accountBlocked:
		return rejected("account-blocked", http.StatusForbidden, "ACCOUNT_BLOCKED", "Account is blocked")
	case accountError:
		return rejected("internal-error", http.StatusInternalServerError, "INTERNAL_ERROR", "Provider failed to process the transaction")
	case accountDown:
		return raw("unavailable", http.StatusServiceUnavailable, "text/plain", "service unavailable")
	case accountGarbage:
		return raw("garbage", http.StatusOK, "application/json", `{"transactionId":"txn-`)
	case accountSlow:
		waitFor(ctx, p.slowDelay)
		return p.apply("slow", request, amount)
	default:
		return p.apply("normal", request, amount)
	}
}

func (p *provider) apply(scenario string, request executeRequest, amount int64) outcome {
	balance, ok := p.move(request.AccountID, request.Type, amount)
	if !ok {
		return rejected(scenario+"-insufficient-funds", http.StatusUnprocessableEntity, "INSUFFICIENT_FUNDS", "Account balance is not enough")
	}

	return jsonOutcome(scenario, http.StatusOK, approvedResponse{
		TransactionID: fmt.Sprintf("txn-%06d", p.sequence.Add(1)),
		Status:        statusApproved,
		Balance:       json.Number(formatCents(balance)),
		ExecutedAt:    p.now().UTC().Format(time.RFC3339),
	})
}

func (p *provider) move(accountID, transactionType string, amount int64) (balance int64, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	balance, known := p.balances[accountID]
	if !known {
		balance = p.initialBalance
	}
	if transactionType == typeDebit {
		if amount > balance {
			return balance, false
		}
		balance -= amount
	} else {
		balance += amount
	}
	p.balances[accountID] = balance
	return balance, true
}

func decodeRequest(w http.ResponseWriter, r *http.Request) (executeRequest, int64, error) {
	var request executeRequest
	body := http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	if err := json.NewDecoder(body).Decode(&request); err != nil {
		return executeRequest{}, 0, fmt.Errorf("request body must be valid JSON: %w", err)
	}
	_, _ = io.Copy(io.Discard, body)

	if request.AccountID == "" {
		return request, 0, fmt.Errorf("accountId is required")
	}
	if request.Type != typeCredit && request.Type != typeDebit {
		return request, 0, fmt.Errorf("type must be CREDIT or DEBIT")
	}
	if request.Currency == "" {
		return request, 0, fmt.Errorf("currency is required")
	}
	amount, err := parseCents(request.Amount.String())
	if err != nil || amount == 0 {
		return request, 0, errInvalidAmount
	}
	return request, amount, nil
}

func rejected(scenario string, statusCode int, code, message string) outcome {
	return jsonOutcome(scenario, statusCode, rejectedResponse{Status: statusRejected, Code: code, Message: message})
}

func jsonOutcome(scenario string, statusCode int, payload any) outcome {
	body, _ := json.Marshal(payload)
	return outcome{scenario: scenario, statusCode: statusCode, contentType: "application/json", body: body}
}

func raw(scenario string, statusCode int, contentType, body string) outcome {
	return outcome{scenario: scenario, statusCode: statusCode, contentType: contentType, body: []byte(body)}
}

func waitFor(ctx context.Context, delay time.Duration) {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func errorMessage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
