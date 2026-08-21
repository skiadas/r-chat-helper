package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"time"
)

func (a *App) Run(ctx context.Context) error {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"healthy": true})
	})
	mux.HandleFunc("GET /auth/login", a.handleOIDCLogin)
	mux.HandleFunc("GET /auth/callback", a.handleOIDCCallback)
	mux.HandleFunc("GET /logout", a.handleLogout)

	mux.Handle("GET /api/me", a.authenticate(http.HandlerFunc(a.handleMe)))
	mux.Handle("GET /api/me/sessions", a.authenticate(http.HandlerFunc(a.handleListSessions)))
	mux.Handle("POST /api/me/sessions", a.authenticate(http.HandlerFunc(a.handleCreateSession)))
	mux.Handle("GET /api/me/sessions/{id}/messages", a.authenticate(http.HandlerFunc(a.handleMessages)))
	mux.Handle("POST /api/me/sessions/{id}/messages", a.authenticate(http.HandlerFunc(a.handleSend)))

	mux.Handle("/", uiHandler())

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go a.ratesLoop(ctx)
	go a.initialSync()

	srv := &http.Server{Addr: a.cfg.Addr, Handler: mux}
	log.Printf("r-chat-helper listening on %s", a.cfg.Addr)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	err := srv.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (a *App) ratesLoop(ctx context.Context) {
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := a.SyncRates(ctx); err != nil {
				log.Printf("rates: daily sync failed: %v", err)
			}
		}
	}
}

func (a *App) initialSync() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := a.SyncRates(ctx); err != nil {
		log.Printf("rates: initial sync failed: %v", err)
	}
}

// --- handlers ---

func (a *App) handleMe(w http.ResponseWriter, r *http.Request) {
	c := claimsOf(r)
	if c == nil {
		writeErr(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if c.Role == RoleAdmin {
		writeJSON(w, http.StatusOK, map[string]any{
			"email": c.Email,
			"role":  RoleAdmin,
		})
		return
	}
	s := studentOf(r)
	if s == nil {
		writeErr(w, http.StatusUnauthorized, "authentication required")
		return
	}
	spent, err := a.SpendByStudent(r.Context(), s.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to load usage")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":            s.ID,
		"name":          s.Name,
		"email":         s.Email,
		"role":          RoleStudent,
		"budget_usd":    float64(s.BudgetMicros) / 1e6,
		"spent_usd":     float64(spent) / 1e6,
		"remaining_usd": float64(s.BudgetMicros-spent) / 1e6,
	})
}

func (a *App) handleListSessions(w http.ResponseWriter, r *http.Request) {
	s := studentOf(r)
	if s == nil {
		writeErr(w, http.StatusUnauthorized, "authentication required")
		return
	}
	sessions, err := a.ListSessions(r.Context(), s.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to list sessions")
		return
	}
	spend, err := a.SpendBySession(r.Context(), s.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to load usage")
		return
	}
	out := make([]map[string]any, 0, len(sessions))
	for _, ses := range sessions {
		out = append(out, map[string]any{
			"id":       ses.ID,
			"title":    ses.Title,
			"created":  ses.CreatedAt,
			"cost_usd": float64(spend[ses.ID]) / 1e6,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *App) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	s := studentOf(r)
	if s == nil {
		writeErr(w, http.StatusUnauthorized, "authentication required")
		return
	}
	var req struct {
		Title string `json:"title"`
	}
	_ = readJSON(r, &req)
	ses, err := a.CreateSession(r.Context(), s.ID, req.Title)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to create session")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": ses.ID, "title": ses.Title})
}

func (a *App) handleMessages(w http.ResponseWriter, r *http.Request) {
	s := studentOf(r)
	if s == nil {
		writeErr(w, http.StatusUnauthorized, "authentication required")
		return
	}
	sid := r.PathValue("id")
	if !a.sessionOwned(r.Context(), sid, s.ID) {
		writeErr(w, http.StatusNotFound, "session not found")
		return
	}
	msgs, err := a.Messages(r.Context(), sid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to load messages")
		return
	}
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, map[string]any{"role": m.Role, "text": m.Text})
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *App) handleSend(w http.ResponseWriter, r *http.Request) {
	s := studentOf(r)
	if s == nil {
		writeErr(w, http.StatusUnauthorized, "authentication required")
		return
	}
	sid := r.PathValue("id")
	if !a.sessionOwned(r.Context(), sid, s.ID) {
		writeErr(w, http.StatusNotFound, "session not found")
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if err := readJSON(r, &req); err != nil || req.Text == "" {
		writeErr(w, http.StatusBadRequest, "text required")
		return
	}

	spentBefore, err := a.SpendByStudent(r.Context(), s.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to load usage")
		return
	}
	if s.BudgetMicros > 0 && spentBefore >= s.BudgetMicros {
		writeErr(w, http.StatusForbidden, "budget exhausted; ask your instructor for more")
		return
	}

	// Persist the user turn.
	if err := a.AddMessage(r.Context(), sid, "user", req.Text); err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to save message")
		return
	}

	// Load the full history and send to the upstream with the student's key.
	msgs, err := a.Messages(r.Context(), sid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to load messages")
		return
	}
	resp, err := a.client.send(r.Context(), msgs)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "model request failed: "+err.Error())
		return
	}

	if _, err := a.RecordInteraction(context.Background(), s.ID, sid, a.cfg.LocksModel,
		Tokens{Input: resp.Usage.Input, Output: resp.Usage.Output, CacheRead: resp.Usage.CacheRead}); err != nil {
		log.Printf("cost: failed to record usage for %s/%s: %v", s.ID, sid, err)
	}

	// Persist the assistant turn (including tool outputs inline).
	assistant := resp.Text
	for _, t := range resp.Tools {
		assistant += "\n\n[fetched " + t.InputText + "]\n" + truncate([]byte(t.Output), 4000)
	}
	if assistant == "" {
		assistant = "(no text response)"
	}
	if err := a.AddMessage(r.Context(), sid, "assistant", assistant); err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to save response")
		return
	}

	spentAfter, _ := a.SpendByStudent(r.Context(), s.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"assistant": assistant,
		"cost_usd":  float64(spentAfter-spentBefore) / 1e6,
		"spent_usd": float64(spentAfter) / 1e6,
	})
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func readJSON(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v)
}
