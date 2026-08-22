package controlplane

import (
	"strings"
	"testing"
)

func TestAssistantTextContextSplit(t *testing.T) {
	tr := &turn{
		Text: "Answer body.",
		Tools: []toolResult{
			{InputText: "https://example.com/a", Output: "content a"},
			{InputText: "https://example.com/b", Output: "content b"},
		},
	}
	assistant, context := assistantTextContext(tr)

	if !strings.Contains(assistant, "Answer body.") {
		t.Fatalf("assistant missing answer body: %q", assistant)
	}
	if !strings.Contains(assistant, "(fetched: https://example.com/a, https://example.com/b)") {
		t.Fatalf("assistant missing source line: %q", assistant)
	}
	if strings.Contains(assistant, "content a") {
		t.Fatalf("assistant leaked tool output: %q", assistant)
	}

	if !strings.Contains(context, "Answer body.") {
		t.Fatalf("context missing answer body: %q", context)
	}
	if !strings.Contains(context, "[fetched https://example.com/a]\ncontent a") {
		t.Fatalf("context missing tool output for a: %q", context)
	}
	if !strings.Contains(context, "[fetched https://example.com/b]\ncontent b") {
		t.Fatalf("context missing tool output for b: %q", context)
	}
}

func TestAssistantTextContextEmptyFallbacks(t *testing.T) {
	tr := &turn{Text: ""}
	assistant, context := assistantTextContext(tr)
	if assistant != "(no text response)" {
		t.Fatalf("empty assistant = %q", assistant)
	}
	if context != "(no text response)" {
		t.Fatalf("empty context = %q", context)
	}
}