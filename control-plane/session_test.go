package controlplane

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAcceptsTurn(t *testing.T) {
	if allowed, avail := acceptsTurn(&Session{LastPromptTokens: 99}, 100); !allowed || avail {
		t.Fatalf("below cap: allowed=%v avail=%v, want allowed", allowed, avail)
	}
	if allowed, avail := acceptsTurn(&Session{LastPromptTokens: 100}, 100); allowed || !avail {
		t.Fatalf("at cap: allowed=%v avail=%v, want refused with summary offered", allowed, avail)
	}
	if allowed, avail := acceptsTurn(&Session{LastPromptTokens: 100, HasSummary: true}, 100); allowed || avail {
		t.Fatalf("summarized at cap: allowed=%v avail=%v, want refused without summary", allowed, avail)
	}
	// Disabled cap (0) always admits.
	if allowed, _ := acceptsTurn(&Session{LastPromptTokens: 1 << 30}, 0); !allowed {
		t.Fatal("disabled cap should admit")
	}
	// A nil session (defensive) is admitted.
	if allowed, _ := acceptsTurn(nil, 100); !allowed {
		t.Fatal("nil session should be admitted defensively")
	}
}

// studentToken signs the enrolled student in and returns a bearer token.
func studentToken(t *testing.T, app *App, studentID string) string {
	t.Helper()
	s, err := app.StudentByID(t.Context(), studentID)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := app.issueToken(s.Email, RoleStudent, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func postAuth(t *testing.T, app *App, sid, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/me/sessions/"+sid+"/messages", strings.NewReader(body))
	req.SetPathValue("id", sid)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	app.authenticate(http.HandlerFunc(app.handleSend)).ServeHTTP(rec, req)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return m
}

func TestHandleSendRejectsFullSession(t *testing.T) {
	app := newTestApp(t)
	app.cfg.SessionMaxTokens = 100
	ctx := t.Context()
	if err := app.AddStudent(ctx, "alice", "alice@college.edu", "Alice", 0); err != nil {
		t.Fatal(err)
	}
	tok := studentToken(t, app, "alice")

	ses, err := app.CreateSession(ctx, "alice", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.SetSessionPromptTokens(ctx, ses.ID, 100); err != nil {
		t.Fatal(err)
	}
	rec := postAuth(t, app, ses.ID, tok, `{"text":"hi"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("full session status = %d, want 409", rec.Code)
	}
	m := decodeBody(t, rec)
	if m["code"] != "session_full" {
		t.Fatalf("body = %s, want code session_full", rec.Body.String())
	}
	if m["summary_available"] != true {
		t.Fatalf("summary_available = %v, want true for a plain session", m["summary_available"])
	}
	// The rejected message must not have been persisted.
	if msgs, err := app.Messages(ctx, ses.ID); err != nil || len(msgs) != 0 {
		t.Fatalf("rejected send persisted a message: %+v err=%v", msgs, err)
	}
}

func TestHandleSendFullSessionSummaryNotOfferedAfterSummarize(t *testing.T) {
	app := newTestApp(t)
	app.cfg.SessionMaxTokens = 10
	ctx := t.Context()
	if err := app.AddStudent(ctx, "bob", "bob@college.edu", "Bob", 0); err != nil {
		t.Fatal(err)
	}
	tok := studentToken(t, app, "bob")

	from, err := app.CreateSessionWithSummary(ctx, "bob", "", "some carried summary")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.SetSessionPromptTokens(ctx, from.ID, 10); err != nil {
		t.Fatal(err)
	}
	rec := postAuth(t, app, from.ID, tok, `{"text":"hi"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("full summarized session status = %d, want 409", rec.Code)
	}
	if decodeBody(t, rec)["summary_available"] != false {
		t.Fatalf("summary_available = %s, want false (once-only rule)", rec.Body.String())
	}
}

func TestHandleStartFromTopicCopiesLastExchange(t *testing.T) {
	app := newTestApp(t)
	ctx := t.Context()
	if err := app.AddStudent(ctx, "alice", "alice@college.edu", "Alice", 0); err != nil {
		t.Fatal(err)
	}
	tok := studentToken(t, app, "alice")

	src, err := app.CreateSession(ctx, "alice", "")
	if err != nil {
		t.Fatal(err)
	}
	msgs := []struct{ role, text string }{
		{"user", "why is lm slow"}, {"assistant", "collinearity"},
		{"user", "now about ggplot2"}, {"assistant", "aes is your friend"},
	}
	for _, m := range msgs {
		if err := app.AddMessage(ctx, src.ID, m.role, m.text, ""); err != nil {
			t.Fatal(err)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/api/me/sessions/from-topic",
		strings.NewReader(`{"source_id":"`+src.ID+`"}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	app.authenticate(http.HandlerFunc(app.handleStartFromTopic)).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("from-topic status = %d body %s", rec.Code, rec.Body.String())
	}
	newID := decodeBody(t, rec)["id"].(string)

	got, err := app.Messages(ctx, newID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("copied %d messages, want the last 2 only", len(got))
	}
	if got[0].Text != "now about ggplot2" || got[0].Role != "user" {
		t.Fatalf("first copied message = %+v", got[0])
	}
	if got[1].Text != "aes is your friend" || got[1].Role != "assistant" {
		t.Fatalf("second copied message = %+v", got[1])
	}
	ses, err := app.Session(ctx, newID)
	if err != nil {
		t.Fatal(err)
	}
	if ses.HasSummary || ses.Summary != "" {
		t.Fatalf("from-topic session should carry no summary: %+v", ses)
	}
}

func TestHandleStartFromSummaryGeneratesAndSeedsSummary(t *testing.T) {
	app := newTestApp(t)
	ctx := t.Context()
	if err := app.AddStudent(ctx, "alice", "alice@college.edu", "Alice", 0); err != nil {
		t.Fatal(err)
	}
	tok := studentToken(t, app, "alice")

	// Point the client at a fake upstream that returns a summary.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "carried: singular fit on lm(y~x at college x)"},
			}},
			"usage": map[string]any{"prompt_tokens": 30, "completion_tokens": 5},
		})
	}))
	defer srv.Close()
	cfg := DefaultConfig()
	cfg.Upstream = srv.URL
	cfg.ProviderKey = "k"
	cfg.WebFetchEnabled = false
	app.client = newGoClient(cfg)

	src, err := app.CreateSession(ctx, "alice", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.AddMessage(ctx, src.ID, "user", "lm is giving me a singular fit", ""); err != nil {
		t.Fatal(err)
	}
	if err := app.AddMessage(ctx, src.ID, "assistant", "drop collinear predictors", ""); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/me/sessions/from-summary",
		strings.NewReader(`{"source_id":"`+src.ID+`"}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	app.authenticate(http.HandlerFunc(app.handleStartFromSummary)).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("from-summary status = %d body %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["summarized"] != true {
		t.Fatalf("summarized = %v, body %s", body["summarized"], rec.Body.String())
	}
	newID := body["id"].(string)
	ses, err := app.Session(ctx, newID)
	if err != nil {
		t.Fatal(err)
	}
	if !ses.HasSummary || !strings.Contains(ses.Summary, "singular fit") {
		t.Fatalf("new summarized session = %+v", ses)
	}
}

func TestHandleStartFromSummaryRespectsOnceOnlyAndOwnership(t *testing.T) {
	app := newTestApp(t)
	ctx := t.Context()
	if err := app.AddStudent(ctx, "alice", "alice@college.edu", "Alice", 0); err != nil {
		t.Fatal(err)
	}
	if err := app.AddStudent(ctx, "bob", "bob@college.edu", "Bob", 0); err != nil {
		t.Fatal(err)
	}
	tokAlice := studentToken(t, app, "alice")

	// A session already created from a summary cannot be summarized again.
	summed, err := app.CreateSessionWithSummary(ctx, "alice", "", "orig")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.AddMessage(ctx, summed.ID, "user", "hi", ""); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/me/sessions/from-summary",
		strings.NewReader(`{"source_id":"`+summed.ID+`"}`))
	req.Header.Set("Authorization", "Bearer "+tokAlice)
	rec := httptest.NewRecorder()
	app.authenticate(http.HandlerFunc(app.handleStartFromSummary)).ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("re-summarize status = %d, want 409", rec.Code)
	}

	// Another student's session is not a valid source.
	bobSes, err := app.CreateSession(ctx, "bob", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.AddMessage(ctx, bobSes.ID, "user", "mine", ""); err != nil {
		t.Fatal(err)
	}
	req2 := httptest.NewRequest(http.MethodPost, "/api/me/sessions/from-summary",
		strings.NewReader(`{"source_id":"`+bobSes.ID+`"}`))
	req2.Header.Set("Authorization", "Bearer "+tokAlice)
	rec2 := httptest.NewRecorder()
	app.authenticate(http.HandlerFunc(app.handleStartFromSummary)).ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusNotFound {
		t.Fatalf("cross-student summary status = %d, want 404", rec2.Code)
	}
}
