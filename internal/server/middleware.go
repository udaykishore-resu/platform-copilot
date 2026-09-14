package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"
)

type ctxKey string

const keyRequestID ctxKey = "request_id"

func requestID(ctx context.Context) string {
	id, _ := ctx.Value(keyRequestID).(string)
	return id
}

// withRequestID attaches an ID to every request so a model call, its audit
// log line and the user's report can be correlated.
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			var b [8]byte
			_, _ = rand.Read(b[:])
			id = hex.EncodeToString(b[:])
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), keyRequestID, id)))
	})
}

// withLogging writes one structured line per request: the audit trail. Note
// what is NOT logged: the question text and the answer. Log prompts only into
// a store with the same access controls as the source documents.
func withLogging(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(rw, r)
		log.Info("request",
			"id", requestID(r.Context()),
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.status,
			"end_user", r.Header.Get("X-End-User-ID"),
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

// withRecover turns panics into 500s instead of dropped connections.
func withRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic", "id", requestID(r.Context()), "err", rec)
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "internal error"})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}
