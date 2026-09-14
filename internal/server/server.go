// Package server exposes the copilot over HTTP so it can sit behind a Slack
// bot, a Backstage plugin or an internal portal. It is deliberately small:
// three endpoints, JSON in and out, end-user IDs propagated to the model
// provider, structured request logs.
//
// Roadmap: Adding end-user IDs in prompts · Safety Best Practices.
package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/udaykishore-resu/platform-copilot/internal/agent"
	"github.com/udaykishore-resu/platform-copilot/internal/rag"
)

// Deps are the collaborators the server needs.
type Deps struct {
	Pipeline *rag.Pipeline
	Agent    *agent.Agent
	Log      *slog.Logger
	Version  string
}

// Server is the HTTP API.
type Server struct {
	deps Deps
	mux  *http.ServeMux
}

// New wires the routes.
func New(d Deps) *Server {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	s := &Server{deps: d, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /healthz", s.health)
	s.mux.HandleFunc("POST /v1/ask", s.ask)
	s.mux.HandleFunc("POST /v1/agent", s.runAgent)
	return s
}

// Handler returns the middleware-wrapped handler.
func (s *Server) Handler() http.Handler {
	return withRequestID(withLogging(s.deps.Log, withRecover(s.mux)))
}

// ListenAndServe starts the server with sane timeouts.
func (s *Server) ListenAndServe(addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      5 * time.Minute, // LLM calls are slow
		IdleTimeout:       60 * time.Second,
	}
	s.deps.Log.Info("listening", "addr", addr)
	return srv.ListenAndServe()
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": s.deps.Version})
}

type askRequest struct {
	Question string `json:"question"`
	// User is the end-user identifier from the calling system (Slack user ID,
	// SSO subject). It is forwarded to the model provider for abuse detection
	// and recorded in the audit log. Never a raw email.
	User string `json:"user"`
}

func (s *Server) ask(w http.ResponseWriter, r *http.Request) {
	var req askRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON: " + err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	p := *s.deps.Pipeline
	p.User = userOr(req.User, r)
	ans, err := p.Ask(ctx, req.Question, nil)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": err.Error()})
		return
	}
	sources := make([]map[string]any, 0, len(ans.Sources))
	for i, h := range ans.Sources {
		sources = append(sources, map[string]any{"n": i + 1, "source": h.Metadata["source"], "section": h.Metadata["section"], "score": h.Score})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"answer": ans.Text, "sources": sources, "model": ans.Model, "usage": ans.Usage,
		"cost": ans.Cost, "latency_ms": ans.Latency.Milliseconds(), "warnings": ans.Warnings,
		"request_id": requestID(r.Context()),
	})
}

type agentRequest struct {
	Task string `json:"task"`
	Mode string `json:"mode"` // react | native
	User string `json:"user"`
}

func (s *Server) runAgent(w http.ResponseWriter, r *http.Request) {
	var req agentRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON: " + err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Minute)
	defer cancel()
	a := *s.deps.Agent
	a.User = userOr(req.User, r)
	if req.Mode != "" {
		a.Mode = agent.Mode(req.Mode)
	}
	res, err := a.Run(ctx, req.Task)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": err.Error(), "partial": res})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": res, "request_id": requestID(r.Context())})
}

func userOr(u string, r *http.Request) string {
	if u != "" {
		return u
	}
	if h := r.Header.Get("X-End-User-ID"); h != "" {
		return h
	}
	return "anonymous"
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
