package logging

import (
	"context"
	"io"
	"log/slog"
)

const requestIDAttr = "requestId"

type requestIDKey struct{}

func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, requestID)
}

func RequestID(ctx context.Context) string {
	requestID, _ := ctx.Value(requestIDKey{}).(string)
	return requestID
}

func New(output io.Writer, level slog.Leveler) *slog.Logger {
	jsonHandler := slog.NewJSONHandler(output, &slog.HandlerOptions{Level: level})
	return slog.New(&contextHandler{Handler: jsonHandler})
}

type contextHandler struct {
	slog.Handler
}

// Adds the request id from the context, so no caller has to pass it by hand.
func (h *contextHandler) Handle(ctx context.Context, record slog.Record) error {
	if requestID := RequestID(ctx); requestID != "" {
		record.AddAttrs(slog.String(requestIDAttr, requestID))
	}
	return h.Handler.Handle(ctx, record)
}

func (h *contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &contextHandler{Handler: h.Handler.WithAttrs(attrs)}
}

func (h *contextHandler) WithGroup(name string) slog.Handler {
	return &contextHandler{Handler: h.Handler.WithGroup(name)}
}
