package logging_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/vit3ro/transaction-execution-api/internal/logging"
)

func lastEntry(t *testing.T, output *bytes.Buffer) map[string]any {
	t.Helper()
	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &entry); err != nil {
		t.Fatalf("log output %q is not a single JSON entry: %v", output.String(), err)
	}
	return entry
}

func TestLogger_AddsRequestIDFromContext(t *testing.T) {
	var output bytes.Buffer
	logger := logging.New(&output, slog.LevelInfo)
	ctx := logging.WithRequestID(context.Background(), "req-123")

	logger.InfoContext(ctx, "provider execution failed", "failureCode", "PROVIDER_TIMEOUT")

	entry := lastEntry(t, &output)
	if entry["requestId"] != "req-123" {
		t.Errorf("requestId = %v, want req-123", entry["requestId"])
	}
	if entry["msg"] != "provider execution failed" || entry["failureCode"] != "PROVIDER_TIMEOUT" {
		t.Errorf("entry = %v, want the original message and attributes", entry)
	}
}

func TestLogger_OmitsRequestIDOutsideARequest(t *testing.T) {
	var output bytes.Buffer
	logger := logging.New(&output, slog.LevelInfo)

	logger.InfoContext(context.Background(), "server listening")

	if _, present := lastEntry(t, &output)["requestId"]; present {
		t.Error("requestId must be absent when the context carries none")
	}
}

func TestLogger_KeepsRequestIDOnDerivedLoggers(t *testing.T) {
	var output bytes.Buffer
	logger := logging.New(&output, slog.LevelInfo).With("component", "providerclient")
	ctx := logging.WithRequestID(context.Background(), "req-456")

	logger.WarnContext(ctx, "retrying provider request")

	entry := lastEntry(t, &output)
	if entry["requestId"] != "req-456" || entry["component"] != "providerclient" {
		t.Errorf("entry = %v, want requestId req-456 and component providerclient", entry)
	}
}

func TestLogger_HonoursLevel(t *testing.T) {
	var output bytes.Buffer
	logger := logging.New(&output, slog.LevelWarn)

	logger.Info("ignored")

	if output.Len() != 0 {
		t.Errorf("output = %q, want nothing below the configured level", output.String())
	}
}

func TestRequestID_EmptyWhenMissing(t *testing.T) {
	if got := logging.RequestID(context.Background()); got != "" {
		t.Errorf("RequestID = %q, want empty", got)
	}
}
