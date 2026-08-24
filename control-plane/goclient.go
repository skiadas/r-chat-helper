package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const defaultUpstream = "https://opencode.ai/zen/go/v1"

// goClient talks to the shared class upstream (opencode.ai/zen/go/v1) as an
// OpenAI-compatible chat/completions endpoint, injecting the configured class
// key as the Bearer token. One client serves all students.
type goClient struct {
	baseURL  string
	model    string
	key      string
	webfetch *webFetcher
	httpc    *http.Client
	maxTools int
}

func newGoClient(cfg *Config) *goClient {
	up := cfg.Upstream
	if up == "" {
		up = defaultUpstream
	}
	c := &goClient{
		baseURL:  up,
		model:    cfg.LocksModel,
		key:      cfg.ProviderKey,
		httpc:    &http.Client{Timeout: 0}, // streaming/agentic turns can be long
		maxTools: 12,
	}
	if cfg.WebFetchEnabled {
		c.webfetch = newWebFetcher(cfg)
	}
	return c
}

// usage aggregates the token accounting from a chat response.
type usage struct {
	Input     int64
	Output    int64
	CacheRead int64
}

// toolResult is an ongoing tool execution; returned to the caller for
// display and accumulated as a conversation turn.
type toolResult struct {
	InputText string // the requested tool call (url) for display
	Output    string // the tool's return value
}

// turn is one model invocation result: the assistant text and any tool calls
// that were made and executed. NewTopic is set when the model signalled that
// the current prompt starts a topic unrelated to the conversation; it is a
// suggestion, never an action, so the student decides whether to start fresh.
type turn struct {
	Text     string
	Tools    []toolResult
	Usage    usage
	NewTopic bool
}

// chatReq mirrors the OpenAI-compatible request fields we send.
type chatReq struct {
	Model      string        `json:"model"`
	Messages   []chatMessage `json:"messages"`
	Tools      []toolDef     `json:"tools,omitempty"`
	ToolChoice string        `json:"tool_choice,omitempty"`
	Stream     bool          `json:"stream,omitempty"`
	MaxTokens  *int          `json:"max_tokens,omitempty"`
}

type chatMessage struct {
	Role       string        `json:"role"`
	Content    string        `json:"content"`
	ToolCalls  []toolCallMsg `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
	Name       string        `json:"name,omitempty"`
}

type toolDef struct {
	Type     string `json:"type"`
	Function fnDef  `json:"function"`
}

type fnDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type toolCallMsg struct {
	ID       string     `json:"id"`
	Type     string     `json:"type"`
	Function toolCallFn `json:"function"`
}

type toolCallFn struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatResp struct {
	// reasoning_content from the upstream is intentionally not parsed: it is
	// the model's chain-of-thought, which students should not see, and its
	// tokens are billed as output tokens, so the cost engine accounts for it.
	ID      string `json:"id"`
	Choices []struct {
		Message struct {
			Role      string        `json:"role"`
			Content   *string       `json:"content"`
			ToolCalls []toolCallMsg `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens        int64 `json:"prompt_tokens"`
		CompletionTokens    int64 `json:"completion_tokens"`
		PromptTokensDetails struct {
			CachedTokens int64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// tools returns the tool definitions offered to the model. suggest_new_topic
// is a signalling tool: it is never executed, and the client's send loop only
// records that it was requested.
func (c *goClient) tools() []toolDef {
	tools := []toolDef{{
		Type: "function",
		Function: fnDef{
			Name:        "suggest_new_topic",
			Description: "Call this when the student's latest question starts a genuinely new topic unrelated to the current conversation. Do not call it for follow-up questions, clarifications, or continuations of the current topic.",
			Parameters:  map[string]any{"type": "object"},
		},
	}}
	if c.webfetch == nil {
		return tools
	}
	return append(tools, toolDef{
		Type: "function",
		Function: fnDef{
			Name:        "webfetch",
			Description: "Fetch the text content of a URL. Use to retrieve current documentation (CRAN, tidyverse, rdrr.io) or any web page whose contents you need.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"url": map[string]any{"type": "string", "description": "the URL to fetch"},
				},
				"required": []string{"url"},
			},
		},
	})
}

// toOpenAIMessages converts persisted chat messages plus any accumulated tool
// turns into the OpenAI message sequence. A message's Context field, when set,
// is what the model sees (full tool output); its Text stays for the UI. A
// non-empty summary is injected as a synthetic system block so a session
// started from a summarized one still has its thread in context.
func (c *goClient) toOpenAIMessages(msgs []Message, tools []toolResult, summary string) []chatMessage {
	out := make([]chatMessage, 0, len(msgs)+len(tools)*2+2)
	out = append(out, chatMessage{Role: "system", Content: systemPrompt})
	if summary != "" {
		out = append(out, chatMessage{Role: "system", Content: "[Prior context] " + summary})
	}
	for _, m := range msgs {
		content := m.Text
		if m.Context != "" {
			content = m.Context
		}
		out = append(out, chatMessage{Role: m.Role, Content: content})
	}
	for _, t := range tools {
		// pretend a single synthetic "user" tool result turn appended after the
		// last message so the model sees the fetched content in-context.
		block := "Tool result (webfetch " + t.InputText + "):\n\n" + t.Output
		out = append(out, chatMessage{Role: "user", Content: block})
	}
	return out
}

const systemPrompt = `You are a friendly R programming tutor for a university statistics course. You help students spot errors in R code they paste, explain how R functions and packages work, and point them to authoritative documentation (CRAN, tidyverse, rdrr.io) and explain it.

The course runs R 4.5.3 with the hanoverbase package, which loads the tidyverse, mosaic, RColorBrewer and lattice packages and provides some course data sets. Prefer functions from that stack; students can use hanoverbase's data sets. If an answer genuinely requires a package outside the stack, say so and note it would need to be installed.

You never run code. If you want to verify how a current package works or fetch live documentation, use the webfetch tool. Otherwise answer from your knowledge; if something is uncertain, say so in a short sentence.

If the student's latest question clearly starts a new topic unrelated to the current conversation, call the suggest_new_topic function; otherwise continue in the current conversation.

Keep answers short and focused. Answer the specific question asked; use at most one minimal code example; point to documentation rather than quoting it at length. Do not add alternative approaches, extra examples, caveats, or "other variants" sections unless the student explicitly asks. If extra depth would be useful, offer it in a single closing sentence instead.`

// send runs one full turn: it may loop to execute webfetch calls the model
// requests, bounded by maxTools. suggest_new_topic is a signal, never
// executed; the turn records it as NewTopic.
func (c *goClient) send(ctx context.Context, msgs []Message, summary string) (*turn, error) {
	var tools []toolResult
	var total usage
	var newTopic bool

	for i := 0; i < c.maxTools; i++ {
		req, err := c.buildRequest(ctx, msgs, tools, summary)
		if err != nil {
			return nil, err
		}
		resp, err := c.doOnce(ctx, req)
		if err != nil {
			return nil, err
		}
		total = sumUsage(total, usageFromResp(resp))

		choice := firstChoice(resp)
		if choice == nil {
			return &turn{Text: "", Usage: total, NewTopic: newTopic}, nil
		}

		var text string
		if choice.Message.Content != nil {
			text = *choice.Message.Content
		}

		if len(choice.Message.ToolCalls) == 0 {
			return &turn{Text: text, Tools: tools, Usage: total, NewTopic: newTopic}, nil
		}

		// Execute each requested tool (webfetch is executed; suggest_new_topic
		// is only recorded).
		for _, tc := range choice.Message.ToolCalls {
			switch tc.Function.Name {
			case "suggest_new_topic":
				newTopic = true
			case "webfetch":
				var args struct {
					URL string `json:"url"`
				}
				_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
				out, err := c.webfetch.Fetch(ctx, args.URL)
				if err != nil {
					out = "webfetch error: " + err.Error()
				}
				tools = append(tools, toolResult{InputText: args.URL, Output: out})
			default:
				tools = append(tools, toolResult{InputText: tc.Function.Name, Output: "unknown tool"})
			}
		}
		// Continue the loop so the model can use the fetched content.
	}

	return &turn{Text: "Stopped: too many tool calls.", Tools: tools, Usage: total, NewTopic: newTopic}, nil
}

// titleFor asks the model for a short conversation title from the given
// messages. It uses a tiny max_tokens cap and no tools so it stays cheap; the
// returned usage is priced like any other interaction. On failure it returns
// an empty title (the session simply stays unnamed).
func (c *goClient) titleFor(ctx context.Context, msgs []Message) (*turn, error) {
	maxTokens := 32
	reqMsgs := make([]chatMessage, 0, len(msgs)+2)
	reqMsgs = append(reqMsgs, chatMessage{Role: "system", Content: "You suggest concise titles for R tutoring conversations."})
	for _, m := range msgs {
		reqMsgs = append(reqMsgs, chatMessage{Role: m.Role, Content: truncate([]byte(m.Text), 500)})
	}
	reqMsgs = append(reqMsgs, chatMessage{Role: "user", Content: "Give this conversation a short title under 50 characters that captures its topic. Reply with only the title."})
	resp, err := c.post(ctx, chatReq{Model: c.model, Messages: reqMsgs, Stream: false, MaxTokens: &maxTokens})
	if err != nil {
		return nil, err
	}
	choice := firstChoice(resp)
	text := ""
	if choice != nil && choice.Message.Content != nil {
		text = strings.TrimSpace(*choice.Message.Content)
	}
	return &turn{Text: text, Usage: usageFromResp(resp)}, nil
}

// summaryFor asks the model for a compact carry-forward summary of a
// conversation, used to seed a fresh session started from this one. It uses a
// small max_tokens cap and no tools so it stays cheap; the returned usage is
// priced like any other interaction. On failure it returns an error and the
// caller falls back to a cold start. The summary is best-effort: the model is
// told to preserve exact artifacts and to mark anything uncertain rather than
// guess, so the tutor that reads it later asks the student to re-paste rather
// than over-claim.
func (c *goClient) summaryFor(ctx context.Context, msgs []Message) (*turn, error) {
	maxTokens := 256
	reqMsgs := make([]chatMessage, 0, len(msgs)+2)
	reqMsgs = append(reqMsgs, chatMessage{Role: "system", Content: "You write concise carry-forward summaries of R tutoring conversations so the tutor can continue helping without the full history. Preserve exact artifacts: variable names, code snippets, error messages, packages and data sets, and any unresolved threads. The summary is best-effort: if a detail is uncertain, say so instead of guessing."})
	for _, m := range msgs {
		reqMsgs = append(reqMsgs, chatMessage{Role: m.Role, Content: truncate([]byte(m.Text), 2000)})
	}
	reqMsgs = append(reqMsgs, chatMessage{Role: "user", Content: "Write a summary of this R tutoring conversation for a continuing session. Keep it under roughly 200 words. Preserve exact code, variable names, error messages, packages, and data sets, and note any unanswered questions. Mark anything uncertain as uncertain rather than guessing."})
	resp, err := c.post(ctx, chatReq{Model: c.model, Messages: reqMsgs, Stream: false, MaxTokens: &maxTokens})
	if err != nil {
		return nil, err
	}
	choice := firstChoice(resp)
	text := ""
	if choice != nil && choice.Message.Content != nil {
		text = strings.TrimSpace(*choice.Message.Content)
	}
	return &turn{Text: text, Usage: usageFromResp(resp)}, nil
}

// post marshals a request body, hits /chat/completions with the class key, and
// returns the parsed response.
func (c *goClient) post(ctx context.Context, body chatReq) (*chatResp, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", strings.NewReader(string(b)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.key)
	return c.doOnce(ctx, req)
}

func (c *goClient) buildRequest(ctx context.Context, msgs []Message, tools []toolResult, summary string) (*http.Request, error) {
	body := chatReq{
		Model:    c.model,
		Messages: c.toOpenAIMessages(msgs, tools, summary),
		Tools:    c.tools(),
		Stream:   false,
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", strings.NewReader(string(b)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.key)
	return req, nil
}

func (c *goClient) doOnce(ctx context.Context, req *http.Request) (*chatResp, error) {
	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	var out chatResp
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse upstream response: %w", err)
	}
	if resp.StatusCode >= 300 {
		msg := "upstream error"
		if out.Error != nil && out.Error.Message != "" {
			msg = out.Error.Message
		} else {
			msg = truncate(raw, 300)
		}
		return nil, fmt.Errorf("upstream %s: %s", resp.Status, msg)
	}
	return &out, nil
}

func firstChoice(r *chatResp) *struct {
	Message struct {
		Role      string        `json:"role"`
		Content   *string       `json:"content"`
		ToolCalls []toolCallMsg `json:"tool_calls"`
	} `json:"message"`
	FinishReason string `json:"finish_reason"`
} {
	if r == nil || len(r.Choices) == 0 {
		return nil
	}
	return &r.Choices[0]
}

func sumUsage(a, b usage) usage {
	return usage{Input: a.Input + b.Input, Output: a.Output + b.Output, CacheRead: a.CacheRead + b.CacheRead}
}

func usageFromResp(r *chatResp) usage {
	if r == nil {
		return usage{}
	}
	return usage{
		Input:     r.Usage.PromptTokens,
		Output:    r.Usage.CompletionTokens,
		CacheRead: r.Usage.PromptTokensDetails.CachedTokens,
	}
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		b = b[:n]
	}
	return strings.TrimSpace(string(b))
}
