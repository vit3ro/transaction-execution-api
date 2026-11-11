package config_test

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/vit3ro/transaction-execution-api/internal/config"
)

func lookupFrom(values map[string]string) config.LookupFunc {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func requiredOnly() map[string]string {
	return map[string]string{
		"DATABASE_URL":      "postgres://user:pass@localhost:5432/transactions?sslmode=disable",
		"PROVIDER_BASE_URL": "http://localhost:8081",
	}
}

func TestLoad_AppliesDefaults(t *testing.T) {
	cfg, err := config.Load(lookupFrom(requiredOnly()))
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}

	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want INFO", cfg.LogLevel)
	}
	wantHTTP := config.HTTP{Port: 8080, RequestTimeout: 10 * time.Second, ShutdownTimeout: 15 * time.Second}
	if cfg.HTTP != wantHTTP {
		t.Errorf("HTTP = %+v, want %+v", cfg.HTTP, wantHTTP)
	}
	wantDatabase := config.Database{
		URL:             "postgres://user:pass@localhost:5432/transactions?sslmode=disable",
		MaxConns:        20,
		MinConns:        2,
		MaxConnLifetime: 30 * time.Minute,
		MaxConnIdleTime: 5 * time.Minute,
		RunMigrations:   false,
	}
	if cfg.Database != wantDatabase {
		t.Errorf("Database = %+v, want %+v", cfg.Database, wantDatabase)
	}
	wantProvider := config.Provider{
		BaseURL:                    "http://localhost:8081",
		RequestTimeout:             3 * time.Second,
		DialTimeout:                time.Second,
		MaxRetries:                 2,
		RetryBaseDelay:             100 * time.Millisecond,
		RetryMaxDelay:              time.Second,
		MaxIdleConns:               100,
		MaxIdleConnsPerHost:        100,
		IdleConnTimeout:            90 * time.Second,
		BreakerFailureThreshold:    5,
		BreakerOpenTimeout:         30 * time.Second,
		BreakerHalfOpenMaxRequests: 1,
	}
	if cfg.Provider != wantProvider {
		t.Errorf("Provider = %+v, want %+v", cfg.Provider, wantProvider)
	}
}

func TestLoad_ReadsOverrides(t *testing.T) {
	values := requiredOnly()
	values["LOG_LEVEL"] = "debug"
	values["HTTP_PORT"] = "9090"
	values["HTTP_REQUEST_TIMEOUT"] = "20s"
	values["DB_MAX_CONNS"] = "50"
	values["RUN_MIGRATIONS"] = "true"
	values["PROVIDER_REQUEST_TIMEOUT"] = "1500ms"
	values["PROVIDER_MAX_RETRIES"] = "0"
	values["PROVIDER_BREAKER_FAILURE_THRESHOLD"] = "10"

	cfg, err := config.Load(lookupFrom(values))
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}

	if cfg.LogLevel != slog.LevelDebug {
		t.Errorf("LogLevel = %v, want DEBUG", cfg.LogLevel)
	}
	if cfg.HTTP.Port != 9090 || cfg.HTTP.RequestTimeout != 20*time.Second {
		t.Errorf("HTTP = %+v, want port 9090 and a 20s request timeout", cfg.HTTP)
	}
	if cfg.Database.MaxConns != 50 || !cfg.Database.RunMigrations {
		t.Errorf("Database = %+v, want 50 connections and migrations enabled", cfg.Database)
	}
	if cfg.Provider.RequestTimeout != 1500*time.Millisecond || cfg.Provider.MaxRetries != 0 || cfg.Provider.BreakerFailureThreshold != 10 {
		t.Errorf("Provider = %+v, want the overridden timeout, retries and threshold", cfg.Provider)
	}
}

func TestLoad_EmptyValuesFallBackToDefaults(t *testing.T) {
	values := requiredOnly()
	values["HTTP_PORT"] = ""

	cfg, err := config.Load(lookupFrom(values))
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}
	if cfg.HTTP.Port != 8080 {
		t.Errorf("Port = %d, want the default 8080", cfg.HTTP.Port)
	}
}

func TestLoad_RejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name      string
		variable  string
		value     string
		wantError string
	}{
		{"missing database url", "DATABASE_URL", "", "DATABASE_URL is required"},
		{"missing provider url", "PROVIDER_BASE_URL", "", "PROVIDER_BASE_URL is required"},
		{"database url with another scheme", "DATABASE_URL", "mysql://localhost/db", "DATABASE_URL must be a postgres:// URL"},
		{"provider url without scheme", "PROVIDER_BASE_URL", "localhost:8081", "PROVIDER_BASE_URL must be an http:// or https:// URL"},
		{"non numeric port", "HTTP_PORT", "http", "HTTP_PORT must be an integer"},
		{"port out of range", "HTTP_PORT", "70000", "HTTP_PORT must be between 1 and 65535"},
		{"invalid duration", "HTTP_REQUEST_TIMEOUT", "10", "HTTP_REQUEST_TIMEOUT must be a duration"},
		{"negative shutdown timeout", "HTTP_SHUTDOWN_TIMEOUT", "-1s", "HTTP_SHUTDOWN_TIMEOUT must be positive"},
		{"invalid boolean", "RUN_MIGRATIONS", "maybe", "RUN_MIGRATIONS must be true or false"},
		{"unknown log level", "LOG_LEVEL", "verbose", "LOG_LEVEL must be debug, info, warn or error"},
		{"non numeric connections", "DB_MAX_CONNS", "many", "DB_MAX_CONNS must be an integer"},
		{"connections over the 32 bit range", "DB_MAX_CONNS", "99999999999", "DB_MAX_CONNS must be an integer"},
		{"negative breaker threshold", "PROVIDER_BREAKER_FAILURE_THRESHOLD", "-1", "PROVIDER_BREAKER_FAILURE_THRESHOLD must be a non-negative integer"},
		{"no database connections", "DB_MAX_CONNS", "0", "DB_MAX_CONNS must be at least 1"},
		{"min connections over max", "DB_MIN_CONNS", "99", "DB_MIN_CONNS must be between 0 and DB_MAX_CONNS"},
		{"negative retries", "PROVIDER_MAX_RETRIES", "-1", "PROVIDER_MAX_RETRIES must not be negative"},
		{"provider timeout over request timeout", "PROVIDER_REQUEST_TIMEOUT", "30s", "PROVIDER_REQUEST_TIMEOUT must be lower than HTTP_REQUEST_TIMEOUT"},
		{"retry max below base", "PROVIDER_RETRY_MAX_DELAY", "1ms", "PROVIDER_RETRY_MAX_DELAY must not be lower than PROVIDER_RETRY_BASE_DELAY"},
		{"idle conns per host over total", "PROVIDER_MAX_IDLE_CONNS_PER_HOST", "500", "PROVIDER_MAX_IDLE_CONNS_PER_HOST must be between 1 and PROVIDER_MAX_IDLE_CONNS"},
		{"zero breaker threshold", "PROVIDER_BREAKER_FAILURE_THRESHOLD", "0", "PROVIDER_BREAKER_FAILURE_THRESHOLD must be at least 1"},
		{"zero breaker open timeout", "PROVIDER_BREAKER_OPEN_TIMEOUT", "0s", "PROVIDER_BREAKER_OPEN_TIMEOUT must be positive"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values := requiredOnly()
			values[tt.variable] = tt.value

			_, err := config.Load(lookupFrom(values))

			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Errorf("Load error = %v, want it to mention %q", err, tt.wantError)
			}
		})
	}
}

func TestLoad_ReportsEveryProblemAtOnce(t *testing.T) {
	_, err := config.Load(lookupFrom(map[string]string{
		"HTTP_PORT":            "abc",
		"PROVIDER_MAX_RETRIES": "-3",
	}))

	if err == nil {
		t.Fatal("Load must fail with an invalid configuration")
	}
	for _, want := range []string{"DATABASE_URL is required", "PROVIDER_BASE_URL is required", "HTTP_PORT must be an integer", "PROVIDER_MAX_RETRIES must not be negative"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestLoad_DoesNotLeakTheDatabaseURL(t *testing.T) {
	values := requiredOnly()
	values["DATABASE_URL"] = "mysql://admin:super-secret@db.internal/prod"

	_, err := config.Load(lookupFrom(values))

	if err == nil || strings.Contains(err.Error(), "super-secret") {
		t.Errorf("error = %v, want a failure that does not include credentials", err)
	}
}
