package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"time"
)

const (
	minPort = 1
	maxPort = 65535
)

type Config struct {
	LogLevel slog.Level
	HTTP     HTTP
	Database Database
	Provider Provider
}

type HTTP struct {
	Port            int
	RequestTimeout  time.Duration
	ShutdownTimeout time.Duration
}

type Database struct {
	URL             string
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
	RunMigrations   bool
}

type Provider struct {
	BaseURL        string
	RequestTimeout time.Duration
	DialTimeout    time.Duration

	MaxRetries     int
	RetryBaseDelay time.Duration
	RetryMaxDelay  time.Duration

	MaxIdleConns        int
	MaxIdleConnsPerHost int
	IdleConnTimeout     time.Duration

	BreakerFailureThreshold    uint32
	BreakerOpenTimeout         time.Duration
	BreakerHalfOpenMaxRequests uint32
}

type LookupFunc func(name string) (string, bool)

// Load reports every problem at once, so a bad deploy is fixed in one pass.
func Load(lookup LookupFunc) (Config, error) {
	env := &reader{lookup: lookup}

	cfg := Config{
		LogLevel: env.logLevel("LOG_LEVEL", slog.LevelInfo),
		HTTP: HTTP{
			Port:            env.integer("HTTP_PORT", 8080),
			RequestTimeout:  env.duration("HTTP_REQUEST_TIMEOUT", 10*time.Second),
			ShutdownTimeout: env.duration("HTTP_SHUTDOWN_TIMEOUT", 15*time.Second),
		},
		Database: Database{
			URL:             env.required("DATABASE_URL"),
			MaxConns:        env.int32("DB_MAX_CONNS", 20),
			MinConns:        env.int32("DB_MIN_CONNS", 2),
			MaxConnLifetime: env.duration("DB_MAX_CONN_LIFETIME", 30*time.Minute),
			MaxConnIdleTime: env.duration("DB_MAX_CONN_IDLE_TIME", 5*time.Minute),
			RunMigrations:   env.boolean("RUN_MIGRATIONS", false),
		},
		Provider: Provider{
			BaseURL:                    env.required("PROVIDER_BASE_URL"),
			RequestTimeout:             env.duration("PROVIDER_REQUEST_TIMEOUT", 3*time.Second),
			DialTimeout:                env.duration("PROVIDER_DIAL_TIMEOUT", time.Second),
			MaxRetries:                 env.integer("PROVIDER_MAX_RETRIES", 2),
			RetryBaseDelay:             env.duration("PROVIDER_RETRY_BASE_DELAY", 100*time.Millisecond),
			RetryMaxDelay:              env.duration("PROVIDER_RETRY_MAX_DELAY", time.Second),
			MaxIdleConns:               env.integer("PROVIDER_MAX_IDLE_CONNS", 100),
			MaxIdleConnsPerHost:        env.integer("PROVIDER_MAX_IDLE_CONNS_PER_HOST", 100),
			IdleConnTimeout:            env.duration("PROVIDER_IDLE_CONN_TIMEOUT", 90*time.Second),
			BreakerFailureThreshold:    env.uint32("PROVIDER_BREAKER_FAILURE_THRESHOLD", 5),
			BreakerOpenTimeout:         env.duration("PROVIDER_BREAKER_OPEN_TIMEOUT", 30*time.Second),
			BreakerHalfOpenMaxRequests: env.uint32("PROVIDER_BREAKER_HALF_OPEN_MAX_REQUESTS", 1),
		},
	}

	env.errs = append(env.errs, cfg.validate()...)
	if len(env.errs) > 0 {
		return Config{}, fmt.Errorf("invalid configuration: %w", errors.Join(env.errs...))
	}
	return cfg, nil
}

func (c Config) validate() []error {
	var errs []error
	check := func(valid bool, format string, args ...any) {
		if !valid {
			errs = append(errs, fmt.Errorf(format, args...))
		}
	}

	check(c.HTTP.Port >= minPort && c.HTTP.Port <= maxPort, "HTTP_PORT must be between %d and %d", minPort, maxPort)
	check(c.HTTP.RequestTimeout > 0, "HTTP_REQUEST_TIMEOUT must be positive")
	check(c.HTTP.ShutdownTimeout > 0, "HTTP_SHUTDOWN_TIMEOUT must be positive")

	check(c.Database.URL == "" || isURL(c.Database.URL, "postgres", "postgresql"),
		"DATABASE_URL must be a postgres:// URL")
	check(c.Database.MaxConns >= 1, "DB_MAX_CONNS must be at least 1")
	check(c.Database.MinConns >= 0 && c.Database.MinConns <= c.Database.MaxConns,
		"DB_MIN_CONNS must be between 0 and DB_MAX_CONNS")
	check(c.Database.MaxConnLifetime > 0, "DB_MAX_CONN_LIFETIME must be positive")
	check(c.Database.MaxConnIdleTime > 0, "DB_MAX_CONN_IDLE_TIME must be positive")

	check(c.Provider.BaseURL == "" || isURL(c.Provider.BaseURL, "http", "https"),
		"PROVIDER_BASE_URL must be an http:// or https:// URL")
	check(c.Provider.RequestTimeout > 0, "PROVIDER_REQUEST_TIMEOUT must be positive")
	// The provider must time out first; otherwise the request deadline would cut
	// the call before we can record a clean FAILED.
	check(c.Provider.RequestTimeout < c.HTTP.RequestTimeout || c.HTTP.RequestTimeout <= 0,
		"PROVIDER_REQUEST_TIMEOUT must be lower than HTTP_REQUEST_TIMEOUT")
	check(c.Provider.DialTimeout > 0, "PROVIDER_DIAL_TIMEOUT must be positive")
	check(c.Provider.MaxRetries >= 0, "PROVIDER_MAX_RETRIES must not be negative")
	check(c.Provider.RetryBaseDelay > 0, "PROVIDER_RETRY_BASE_DELAY must be positive")
	check(c.Provider.RetryMaxDelay >= c.Provider.RetryBaseDelay,
		"PROVIDER_RETRY_MAX_DELAY must not be lower than PROVIDER_RETRY_BASE_DELAY")
	check(c.Provider.MaxIdleConns >= 1, "PROVIDER_MAX_IDLE_CONNS must be at least 1")
	check(c.Provider.MaxIdleConnsPerHost >= 1 && c.Provider.MaxIdleConnsPerHost <= c.Provider.MaxIdleConns,
		"PROVIDER_MAX_IDLE_CONNS_PER_HOST must be between 1 and PROVIDER_MAX_IDLE_CONNS")
	check(c.Provider.IdleConnTimeout > 0, "PROVIDER_IDLE_CONN_TIMEOUT must be positive")
	check(c.Provider.BreakerFailureThreshold >= 1, "PROVIDER_BREAKER_FAILURE_THRESHOLD must be at least 1")
	check(c.Provider.BreakerOpenTimeout > 0, "PROVIDER_BREAKER_OPEN_TIMEOUT must be positive")
	check(c.Provider.BreakerHalfOpenMaxRequests >= 1, "PROVIDER_BREAKER_HALF_OPEN_MAX_REQUESTS must be at least 1")

	return errs
}

func isURL(value string, schemes ...string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" {
		return false
	}
	for _, scheme := range schemes {
		if parsed.Scheme == scheme {
			return true
		}
	}
	return false
}

type reader struct {
	lookup LookupFunc
	errs   []error
}

func (r *reader) value(name string) (string, bool) {
	value, ok := r.lookup(name)
	return value, ok && value != ""
}

func (r *reader) fail(name, expectation string) {
	r.errs = append(r.errs, fmt.Errorf("%s %s", name, expectation))
}

func (r *reader) required(name string) string {
	value, ok := r.value(name)
	if !ok {
		r.fail(name, "is required")
	}
	return value
}

func (r *reader) integer(name string, fallback int) int {
	value, ok := r.value(name)
	if !ok {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		r.fail(name, "must be an integer")
		return fallback
	}
	return parsed
}

func (r *reader) duration(name string, fallback time.Duration) time.Duration {
	value, ok := r.value(name)
	if !ok {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		r.fail(name, "must be a duration such as 500ms or 3s")
		return fallback
	}
	return parsed
}

func (r *reader) boolean(name string, fallback bool) bool {
	value, ok := r.value(name)
	if !ok {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		r.fail(name, "must be true or false")
		return fallback
	}
	return parsed
}

func (r *reader) logLevel(name string, fallback slog.Level) slog.Level {
	value, ok := r.value(name)
	if !ok {
		return fallback
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(value)); err != nil {
		r.fail(name, "must be debug, info, warn or error")
		return fallback
	}
	return level
}

func (r *reader) int32(name string, fallback int32) int32 {
	value, ok := r.value(name)
	if !ok {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 32)
	if err != nil {
		r.fail(name, "must be an integer")
		return fallback
	}
	return int32(parsed)
}

func (r *reader) uint32(name string, fallback uint32) uint32 {
	value, ok := r.value(name)
	if !ok {
		return fallback
	}
	parsed, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		r.fail(name, "must be a non-negative integer")
		return fallback
	}
	return uint32(parsed)
}
