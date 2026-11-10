package httpapi

import (
	"log/slog"
	"net/http"
	"time"
)

type Dependencies struct {
	Executor       TransactionExecutor
	Lister         TransactionLister
	Readiness      ReadinessChecker
	Logger         *slog.Logger
	RequestTimeout time.Duration
}

func NewRouter(deps Dependencies) http.Handler {
	transactions := &transactionHandler{
		executor: deps.Executor,
		lister:   deps.Lister,
		logger:   deps.Logger,
	}
	health := &healthHandler{readiness: deps.Readiness, logger: deps.Logger}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /transactions", transactions.create)
	mux.HandleFunc("GET /transactions", transactions.list)
	mux.HandleFunc("GET /healthz", health.live)
	mux.HandleFunc("GET /readyz", health.ready)

	return chain(mux,
		withRequestID,
		withLogging(deps.Logger),
		withRecovery(deps.Logger),
		withTimeout(deps.RequestTimeout),
	)
}

func chain(handler http.Handler, middlewares ...func(http.Handler) http.Handler) http.Handler {
	for index := len(middlewares) - 1; index >= 0; index-- {
		handler = middlewares[index](handler)
	}
	return handler
}
