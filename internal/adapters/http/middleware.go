package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/google/uuid"

	"github.com/vit3ro/transaction-execution-api/internal/logging"
)

const (
	headerRequestID    = "X-Request-ID"
	maxRequestIDLength = 128
)

func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get(headerRequestID)
		// Reuse the caller id to follow a request across services, but do not trust
		// an oversized value in our logs.
		if requestID == "" || len(requestID) > maxRequestIDLength {
			requestID = uuid.NewString()
		}
		w.Header().Set(headerRequestID, requestID)
		next.ServeHTTP(w, r.WithContext(logging.WithRequestID(r.Context(), requestID)))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(body []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(body)
}

func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

func withLogging(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			recorder := &statusRecorder{ResponseWriter: w}

			next.ServeHTTP(recorder, r)

			level := slog.LevelInfo
			if recorder.status >= http.StatusInternalServerError {
				level = slog.LevelError
			}
			logger.Log(r.Context(), level, "http request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", recorder.status,
				"durationMs", time.Since(start).Milliseconds(),
			)
		})
	}
}

func withRecovery(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer recoverPanic(w, r, logger)
			next.ServeHTTP(w, r)
		})
	}
}

func recoverPanic(w http.ResponseWriter, r *http.Request, logger *slog.Logger) {
	recovered := recover()
	if recovered == nil {
		return
	}
	if err, isError := recovered.(error); isError && errors.Is(err, http.ErrAbortHandler) {
		panic(recovered)
	}
	logger.ErrorContext(r.Context(), "panic recovered",
		"method", r.Method,
		"path", r.URL.Path,
		"panic", recovered,
		"stack", string(debug.Stack()),
	)
	writeProblem(w, r, http.StatusInternalServerError, codeInternalError, "an unexpected error occurred")
}

// The deadline travels in the context instead of using http.TimeoutHandler,
// so a timeout is still answered in our own error format.
func withTimeout(timeout time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if timeout <= 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
