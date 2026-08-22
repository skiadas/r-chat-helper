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
// that were made and executed.
type turn struct {
	Text  string
	Tools []toolResult
	Usage usage
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

// tools returns the tool definitions offered to the model.
func (c *goClient) tools() []toolDef {
	if c.webfetch == nil {
		return nil
	}
	return []toolDef{{
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
	}}
}

// toOpenAIMessages converts persisted chat messages plus any accumulated tool
// turns into the OpenAI message sequence. A message's Context field, when set,
// is what the model sees (full tool output); its Text stays for the UI.
func (c *goClient) toOpenAIMessages(msgs []Message, tools []toolResult) []chatMessage {
	out := make([]chatMessage, 0, len(msgs)+len(tools)*2)
	out = append(out, chatMessage{Role: "system", Content: systemPrompt})
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

Keep answers short and focused. Answer the specific question asked; use at most one minimal code example; point to documentation rather than quoting it at length. Do not add alternative approaches, extra examples, caveats, or "other variants" sections unless the student explicitly asks. If extra depth would be useful, offer it in a single closing sentence instead.`

// send runs one full turn: it may loop to execute webfetch calls the model
// requests, bounded by maxTools.
func (c *goClient) send(ctx context.Context, msgs []Message) (*turn, error) {
	var tools []toolResult
	var total usage

	for i := 0; i < c.maxTools; i++ {
		req, err := c.buildRequest(ctx, msgs, tools)
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
			return &turn{Text: "", Usage: total}, nil
		}

		var text string
		if choice.Message.Content != nil {
			text = *choice.Message.Content
		}

		if len(choice.Message.ToolCalls) == 0 {
			return &turn{Text: text, Tools: tools, Usage: total}, nil
		}

		// Execute each requested tool (only webfetch is offered).
		for _, tc := range choice.Message.ToolCalls {
			switch tc.Function.Name {
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

	return &turn{Text: "Stopped: too many tool calls.", Tools: tools, Usage: total}, nil
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

func (c *goClient) buildRequest(ctx context.Context, msgs []Message, tools []toolResult) (*http.Request, error) {
	body := chatReq{
		Model:    c.model,
		Messages: c.toOpenAIMessages(msgs, tools),
		Stream:   false,
	}
	if c.webfetch != nil {
		body.Tools = c.tools()
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
