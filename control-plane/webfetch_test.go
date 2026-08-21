package controlplane

import (
	"strings"
	"testing"
)

func TestHTMlToTextStripsBoilerplate(t *testing.T) {
	doc := `<!doctype html><html><head><title>geom_point</title></head>
<body>
<nav>Home Docs About</nav>
<script>alert("x")</script>
<style>.x{}</style>
<main><h1>The title</h1>
<p>This is the <strong>description</strong> of <code>geom_point</code>.</p>
<ul><li>First point</li><li>Second point</li></ul>
<a href="/reference/geom_point.html">Official reference</a>
</main><footer>footer text</footer>
</body></html>`
	got, err := htmlToText(strings.NewReader(doc), 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"The title", "description", "geom_point", "First point", "Second point", "[/reference/geom_point.html]"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	for _, bad := range []string{"<nav>", "Home Docs About", "alert(", ".x{}", "footer text", "<script>", "<style>"} {
		if strings.Contains(got, bad) {
			t.Fatalf("boilerplate leaked (%q) in:\n%s", bad, got)
		}
	}
}

func TestHTMlToTextCapsOutput(t *testing.T) {
	doc := `<html><body><p>` + strings.Repeat("word ", 40) + `</p></body></html>`
	got, err := htmlToText(strings.NewReader(doc), 20)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "[truncated]") {
		t.Fatalf("output not truncated: %q", got)
	}
	if !strings.Contains(got, "word") {
		t.Fatalf("truncated text lost content: %q", got)
	}
}

func TestCollapseText(t *testing.T) {
	got := collapseText("  hello \n\n\n   world \n  \n next")
	want := "hello\nworld\nnext"
	if got != want {
		t.Fatalf("collapseText = %q, want %q", got, want)
	}
}