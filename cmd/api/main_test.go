package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/vit3ro/transaction-execution-api/internal/config"
)

func freeAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a free port: %v", err)
	}
	defer listener.Close()
	return listener.Addr().String()
}

func waitUntilListening(t *testing.T, address string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		connection, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err == nil {
			connection.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server did not start listening on %s", address)
}

func TestServe_WaitsForInFlightRequestsOnShutdown(t *testing.T) {
	address := freeAddress(t)
	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(requestStarted)
		<-releaseRequest
		_, _ = io.WriteString(w, "finished")
	})
	server := &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: time.Second}
	ctx, shutdown := context.WithCancel(context.Background())
	defer shutdown()

	serveResult := make(chan error, 1)
	go func() {
		serveResult <- serve(ctx, server, 5*time.Second, slog.New(slog.DiscardHandler))
	}()
	waitUntilListening(t, address)

	responseBody := make(chan string, 1)
	go func() {
		response, err := http.Get("http://" + address)
		if err != nil {
			responseBody <- "request failed: " + err.Error()
			return
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		responseBody <- string(body)
	}()

	<-requestStarted
	shutdown()

	select {
	case err := <-serveResult:
		t.Fatalf("serve returned %v before the in-flight request finished", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseRequest)

	if body := <-responseBody; body != "finished" {
		t.Errorf("in-flight response = %q, want finished", body)
	}
	if err := <-serveResult; err != nil {
		t.Errorf("serve error = %v, want a clean shutdown", err)
	}
	if _, err := net.DialTimeout("tcp", address, 200*time.Millisecond); err == nil {
		t.Error("server still accepts connections after shutdown")
	}
}

func TestServe_ForcesShutdownAfterTheTimeout(t *testing.T) {
	address := freeAddress(t)
	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	defer close(releaseRequest)
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(requestStarted)
		<-releaseRequest
	})
	server := &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: time.Second}
	ctx, shutdown := context.WithCancel(context.Background())
	defer shutdown()

	serveResult := make(chan error, 1)
	go func() {
		serveResult <- serve(ctx, server, 50*time.Millisecond, slog.New(slog.DiscardHandler))
	}()
	waitUntilListening(t, address)
	go func() {
		response, err := http.Get("http://" + address)
		if err == nil {
			response.Body.Close()
		}
	}()

	<-requestStarted
	shutdown()

	select {
	case err := <-serveResult:
		if err == nil {
			t.Error("serve must report that the graceful shutdown timed out")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("serve did not return after the shutdown timeout")
	}
}

func TestServe_ReportsListenFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	defer listener.Close()
	server := &http.Server{Addr: listener.Addr().String(), ReadHeaderTimeout: time.Second}

	err = serve(context.Background(), server, time.Second, slog.New(slog.DiscardHandler))

	if err == nil {
		t.Error("serve must fail when the port is already in use")
	}
}

func TestNewServer_DerivesTimeoutsFromConfig(t *testing.T) {
	server := newServer(config.HTTP{Port: 9090, RequestTimeout: 10 * time.Second}, http.NotFoundHandler())

	if server.Addr != ":9090" {
		t.Errorf("Addr = %q, want :9090", server.Addr)
	}
	if server.WriteTimeout <= 10*time.Second {
		t.Errorf("WriteTimeout = %v, want it above the request timeout", server.WriteTimeout)
	}
	if server.ReadHeaderTimeout <= 0 || server.IdleTimeout <= 0 {
		t.Error("server must set header and idle timeouts")
	}
}

func TestRun_FailsFastOnInvalidConfiguration(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("PROVIDER_BASE_URL", "")
	t.Setenv("HTTP_PORT", "not-a-port")

	err := run()

	if err == nil {
		t.Fatal("run must fail before starting anything when the configuration is invalid")
	}
	for _, want := range []string{"DATABASE_URL is required", "PROVIDER_BASE_URL is required", "HTTP_PORT must be an integer"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestRun_FailsWhenTheDatabaseIsUnreachable(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:secret-password@"+freeAddress(t)+"/transactions?sslmode=disable&connect_timeout=2")
	t.Setenv("PROVIDER_BASE_URL", "http://localhost:8081")
	t.Setenv("RUN_MIGRATIONS", "false")

	err := run()

	if err == nil {
		t.Fatal("run must fail when the database cannot be reached")
	}
	if strings.Contains(err.Error(), "secret-password") {
		t.Errorf("error leaks the database password: %v", err)
	}
}

func TestProviderConfig_MapsEveryField(t *testing.T) {
	source := config.Provider{
		BaseURL:                    "http://provider.internal",
		RequestTimeout:             3 * time.Second,
		DialTimeout:                time.Second,
		MaxRetries:                 2,
		RetryBaseDelay:             100 * time.Millisecond,
		RetryMaxDelay:              time.Second,
		MaxIdleConns:               100,
		MaxIdleConnsPerHost:        50,
		IdleConnTimeout:            90 * time.Second,
		BreakerFailureThreshold:    5,
		BreakerOpenTimeout:         30 * time.Second,
		BreakerHalfOpenMaxRequests: 1,
	}

	mapped := reflect.ValueOf(providerConfig(source))

	for index := range mapped.NumField() {
		if mapped.Field(index).IsZero() {
			t.Errorf("provider client field %s was left unset", mapped.Type().Field(index).Name)
		}
	}
}
