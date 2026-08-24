package controlplane

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeUpstream is a minimal OpenAI-compatible chat endpoint that records the
// Authorization header and can answer with a tool call then a final answer.
type fakeUpstream struct {
	mu         sync.Mutex
	authHeader string
	models     []string
	requests   int
	toolResult string
	fetchBody  string
}

func (f *fakeUpstream) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.authHeader = r.Header.Get("Authorization")
		f.requests++
		defer f.mu.Unlock()

		var req chatReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		f.models = append(f.models, req.Model)

		// If the model called webfetch, respond with a final answer containing
		// the fetched content.
		if f.requests == 2 {
			content := "looked up docs, resolved your error"
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{
					"message": map[string]any{"role": "assistant", "content": content},
				}},
				"usage": map[string]any{
					"prompt_tokens":     100,
					"completion_tokens": 20,
				},
			})
			return
		}

		// First request: return a webfetch tool call.
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"role":    "assistant",
					"content": nil,
					"tool_calls": []map[string]any{{
						"id":   "call_1",
						"type": "function",
						"function": map[string]any{
							"name":      "webfetch",
							"arguments": `{"url":"https://rdrr.io/api/docs"}`,
						},
					}},
				},
			}},
			"usage": map[string]any{
				"prompt_tokens":     10,
				"completion_tokens": 5,
			},
		})
	})
}

func TestClientInjectsConfiguredKeyAndForcesModel(t *testing.T) {
	f := &fakeUpstream{}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.Upstream = srv.URL
	cfg.ProviderKey = "sk-class-xyz"
	cfg.WebFetchEnabled = true
	c := newGoClient(cfg)

	tr, err := c.send(t.Context(), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.authHeader != "Bearer sk-class-xyz" {
		t.Fatalf("auth header = %q, want the configured class key", f.authHeader)
	}
	if len(f.models) == 0 || f.models[0] != LockedModelID {
		t.Fatalf("model = %v, want locked model", f.models)
	}
	if tr.Text == "" || !strings.Contains(tr.Text, "looked up docs") {
		t.Fatalf("unexpected final text: %q", tr.Text)
	}
	if len(tr.Tools) != 1 || tr.Tools[0].InputText != "https://rdrr.io/api/docs" {
		t.Fatalf("expected one webfetch tool result, got %+v", tr.Tools)
	}
	if tr.Usage.Input == 0 || tr.Usage.Output == 0 {
		t.Fatalf("usage not accumulated: %+v", tr.Usage)
	}
}

func TestToOpenAIMessagesUsesContextWhenSet(t *testing.T) {
	c := newGoClient(DefaultConfig())
	msgs := []Message{
		{Role: "user", Text: "hi"},
		{Role: "assistant", Text: "answer (display only)", Context: "answer\n\n[fetched url]\nraw fetched content"},
	}
	out := c.toOpenAIMessages(msgs, nil, "")
	if len(out) != 3 { // system + user + assistant
		t.Fatalf("got %d messages, want 3", len(out))
	}
	if out[1].Role != "user" || out[1].Content != "hi" {
		t.Fatalf("user message wrong: %+v", out[1])
	}
	if out[2].Content != "answer\n\n[fetched url]\nraw fetched content" {
		t.Fatalf("assistant did not use Context (tool output) for the model: %q", out[2].Content)
	}
	if strings.Contains(out[2].Content, "display only") {
		t.Fatalf("assistant Context leaked the display-only text: %q", out[2].Content)
	}
}

func TestToOpenAIMessagesPrependsCarriedSummary(t *testing.T) {
	c := newGoClient(DefaultConfig())
	out := c.toOpenAIMessages([]Message{{Role: "user", Text: "hi"}}, nil, "student was fitting lm()")
	if len(out) != 3 {
		t.Fatalf("got %d messages, want 3 (system, summary, user)", len(out))
	}
	if out[0].Role != "system" || !strings.HasPrefix(out[0].Content, "You are a friendly R programming tutor") {
		t.Fatalf("first message should be the system prompt: %+v", out[0])
	}
	if out[1].Role != "system" || !strings.Contains(out[1].Content, "student was fitting lm()") {
		t.Fatalf("summary block missing: %+v", out[1])
	}
	// An empty summary must not inject anything.
	plain := c.toOpenAIMessages([]Message{{Role: "user", Text: "hi"}}, nil, "")
	if len(plain) != 2 {
		t.Fatalf("without summary got %d messages, want 2", len(plain))
	}
}

// newTopicUpstream returns a suggest_new_topic signal on the first request and
// a final answer on the second, recording whether the signal was ever executed
// as a tool result.
type newTopicUpstream struct {
	requests int
}

func (f *newTopicUpstream) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests++
		if f.requests == 1 {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{
					"message": map[string]any{
						"role":    "assistant",
						"content": nil,
						"tool_calls": []map[string]any{{
							"id":   "call_1",
							"type": "function",
							"function": map[string]any{
								"name":      "suggest_new_topic",
								"arguments": `{}`,
							},
						}},
					},
				}},
				"usage": map[string]any{"prompt_tokens": 5, "completion_tokens": 1},
			})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "sure, that is a new topic"},
			}},
			"usage": map[string]any{"prompt_tokens": 7, "completion_tokens": 6},
		})
	})
}

func TestSendRecordsNewTopicSignalWithoutExecutingIt(t *testing.T) {
	f := &newTopicUpstream{}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.Upstream = srv.URL
	cfg.ProviderKey = "k"
	cfg.WebFetchEnabled = false
	c := newGoClient(cfg)

	tr, err := c.send(t.Context(), []Message{{Role: "user", Text: "now about ggplot2"}, {Role: "assistant", Text: "prev answer"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !tr.NewTopic {
		t.Fatalf("NewTopic not recorded: %+v", tr)
	}
	if tr.Text != "sure, that is a new topic" {
		t.Fatalf("final text = %q", tr.Text)
	}
	// The signal must never surface as an executed tool result.
	if len(tr.Tools) != 0 {
		t.Fatalf("signal leaked into tool results: %+v", tr.Tools)
	}
	if tr.Usage.Input != 12 || tr.Usage.Output != 7 {
		t.Fatalf("usage not accumulated across turns: %+v", tr.Usage)
	}
}

func TestSummaryForReturnsTextAndUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if req.MaxTokens == nil || *req.MaxTokens > 512 {
			t.Fatalf("summary max_tokens = %v, want <= 512", req.MaxTokens)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "student fitting lm() with singular fit errors"},
			}},
			"usage": map[string]any{"prompt_tokens": 40, "completion_tokens": 9},
		})
	}))
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.Upstream = srv.URL
	cfg.ProviderKey = "k"
	c := newGoClient(cfg)

	tr, err := c.summaryFor(t.Context(), []Message{{Role: "user", Text: "lm(y~x)"}})
	if err != nil {
		t.Fatal(err)
	}
	if tr.Text != "student fitting lm() with singular fit errors" {
		t.Fatalf("summary text = %q", tr.Text)
	}
	if tr.Usage.Input != 40 || tr.Usage.Output != 9 {
		t.Fatalf("summary usage = %+v", tr.Usage)
	}
	if len(tr.Tools) != 0 {
		t.Fatalf("summary offered tools: %+v", tr.Tools)
	}
}

func TestSendNoWebFetchStillOffersSuggestNewTopic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatReq
		_ = json.NewDecoder(r.Body).Decode(&req)
		var names []string
		for _, td := range req.Tools {
			names = append(names, td.Function.Name)
		}
		found := false
		for _, n := range names {
			if n == "suggest_new_topic" {
				found = true
			}
		}
		if !found {
			t.Errorf("tools = %v, want suggest_new_topic", names)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "ok"}}},
			"usage":   map[string]any{"prompt_tokens": 1, "completion_tokens": 1},
		})
	}))
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.Upstream = srv.URL
	cfg.ProviderKey = "k"
	cfg.WebFetchEnabled = false
	c := newGoClient(cfg)
	if _, err := c.send(t.Context(), nil, ""); err != nil {
		t.Fatal(err)
	}
}

func TestWebFetchAllowlist(t *testing.T) {
	body := "<html><body>dplyr docs</body></html>"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	defer srv.Close()

	allowedHost := strings.Split(strings.TrimPrefix(srv.URL, "http://"), ":")[0]

	// Allowed host passes.
	cfg := DefaultConfig()
	cfg.WebFetchEnabled = true
	cfg.WebFetchAllowlist = []string{allowedHost}
	wf := newWebFetcher(cfg)
	got, err := wf.Fetch(t.Context(), srv.URL)
	if err != nil {
		t.Fatalf("allowed fetch failed: %v", err)
	}
	if !strings.Contains(got, "dplyr docs") {
		t.Fatalf("fetched body = %q", got)
	}

	// Non-allowlisted host is rejected before any request is made.
	reject := newWebFetcher(DefaultConfig())
	reject.allow = []string{"rdrr.io"}
	if _, err := reject.Fetch(t.Context(), "https://www.r-project.org/doc/"); err == nil {
		t.Fatal("expected error fetching non-allowlisted host")
	} else if !strings.Contains(err.Error(), "allowed") {
		t.Fatalf("unexpected error: %v", err)
	}
}
