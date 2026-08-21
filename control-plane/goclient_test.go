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

	tr, err := c.send(t.Context(), nil)
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
