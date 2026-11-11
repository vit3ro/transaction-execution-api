package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const (
	envPort           = "MOCK_PORT"
	envInitialBalance = "MOCK_INITIAL_BALANCE"
	envSlowDelay      = "MOCK_SLOW_DELAY"

	defaultPort           = "8081"
	defaultInitialBalance = "10000.00"
	defaultSlowDelay      = "5s"

	readHeaderTimeout = 5 * time.Second
	shutdownTimeout   = 10 * time.Second
)

type config struct {
	port           string
	initialBalance int64
	slowDelay      time.Duration
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	if err := run(logger); err != nil {
		logger.Error("mock provider stopped with error", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	server := &http.Server{
		Addr:              ":" + cfg.port,
		Handler:           newProvider(cfg.initialBalance, cfg.slowDelay, logger).routes(),
		ReadHeaderTimeout: readHeaderTimeout,
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("mock provider listening",
			"addr", server.Addr,
			"initialBalance", formatCents(cfg.initialBalance),
			"slowDelay", cfg.slowDelay.String(),
		)
		serverErr <- server.ListenAndServe()
	}()

	select {
	case err := <-serverErr:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}

func loadConfig() (config, error) {
	initialBalance, err := parseCents(envOrDefault(envInitialBalance, defaultInitialBalance))
	if err != nil {
		return config{}, fmt.Errorf("%s: %w", envInitialBalance, err)
	}
	slowDelay, err := time.ParseDuration(envOrDefault(envSlowDelay, defaultSlowDelay))
	if err != nil {
		return config{}, fmt.Errorf("%s: %w", envSlowDelay, err)
	}
	return config{
		port:           envOrDefault(envPort, defaultPort),
		initialBalance: initialBalance,
		slowDelay:      slowDelay,
	}, nil
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
