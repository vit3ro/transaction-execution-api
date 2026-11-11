package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
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
