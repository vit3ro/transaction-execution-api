package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	httpapi "github.com/vit3ro/transaction-execution-api/internal/adapters/http"
	"github.com/vit3ro/transaction-execution-api/internal/adapters/postgres"
	"github.com/vit3ro/transaction-execution-api/internal/adapters/providerclient"
	"github.com/vit3ro/transaction-execution-api/internal/adapters/system"
	"github.com/vit3ro/transaction-execution-api/internal/application"
	"github.com/vit3ro/transaction-execution-api/internal/config"
	"github.com/vit3ro/transaction-execution-api/internal/logging"
)

const (
	readHeaderTimeout  = 5 * time.Second
	readTimeout        = 10 * time.Second
	idleTimeout        = 60 * time.Second
	writeTimeoutMargin = 5 * time.Second
)

func main() {
	if err := run(); err != nil {
		logging.New(os.Stdout, slog.LevelInfo).Error("service stopped with error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.LookupEnv)
	if err != nil {
		return err
	}
	logger := logging.New(os.Stdout, cfg.LogLevel)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Behind a flag: handy for local runs, but in production a schema change
	// should be its own deploy step.
	if cfg.Database.RunMigrations {
		if err := postgres.Migrate(cfg.Database.URL); err != nil {
			return err
		}
		logger.Info("database migrations applied")
	}

	pool, err := postgres.NewPool(ctx, postgres.Config{
		DatabaseURL:     cfg.Database.URL,
		MaxConns:        cfg.Database.MaxConns,
		MinConns:        cfg.Database.MinConns,
		MaxConnLifetime: cfg.Database.MaxConnLifetime,
		MaxConnIdleTime: cfg.Database.MaxConnIdleTime,
	})
	if err != nil {
		return err
	}
	defer func() {
		pool.Close()
		logger.Info("database pool closed")
	}()

	provider, err := providerclient.New(providerConfig(cfg.Provider), logger)
	if err != nil {
		return err
	}

	repository := postgres.NewRepository(pool)
	router := httpapi.NewRouter(httpapi.Dependencies{
		Executor:       application.NewExecuteTransaction(repository, provider, system.Clock{}, system.UUIDGenerator{}, logger),
		Lister:         application.NewListTransactions(repository),
		Readiness:      pool,
		Logger:         logger,
		RequestTimeout: cfg.HTTP.RequestTimeout,
	})

	return serve(ctx, newServer(cfg.HTTP, router), cfg.HTTP.ShutdownTimeout, logger)
}

func providerConfig(cfg config.Provider) providerclient.Config {
	return providerclient.Config{
		BaseURL:                    cfg.BaseURL,
		RequestTimeout:             cfg.RequestTimeout,
		DialTimeout:                cfg.DialTimeout,
		MaxRetries:                 cfg.MaxRetries,
		RetryBaseDelay:             cfg.RetryBaseDelay,
		RetryMaxDelay:              cfg.RetryMaxDelay,
		MaxIdleConns:               cfg.MaxIdleConns,
		MaxIdleConnsPerHost:        cfg.MaxIdleConnsPerHost,
		IdleConnTimeout:            cfg.IdleConnTimeout,
		BreakerFailureThreshold:    cfg.BreakerFailureThreshold,
		BreakerOpenTimeout:         cfg.BreakerOpenTimeout,
		BreakerHalfOpenMaxRequests: cfg.BreakerHalfOpenMaxRequests,
	}
}

func newServer(cfg config.HTTP, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              net.JoinHostPort("", strconv.Itoa(cfg.Port)),
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		// Slightly above the request timeout, so our own 504 is written before the
		// server closes the connection.
		WriteTimeout: cfg.RequestTimeout + writeTimeoutMargin,
		IdleTimeout:  idleTimeout,
	}
}

func serve(ctx context.Context, server *http.Server, shutdownTimeout time.Duration, logger *slog.Logger) error {
	serverErr := make(chan error, 1)
	go func() {
		logger.Info("server listening", "addr", server.Addr)
		serverErr <- server.ListenAndServe()
	}()

	select {
	case err := <-serverErr:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
		logger.Info("shutdown signal received, waiting for in-flight requests")
	}

	// ctx is already cancelled by the signal, so the wait for in-flight requests
	// needs its own deadline.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", errors.Join(err, server.Close()))
	}
	logger.Info("server stopped gracefully")
	return nil
}
