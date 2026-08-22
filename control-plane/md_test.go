package controlplane

import (
	"strings"
	"testing"
)

func TestRenderMarkdownRLabelAndLangs(t *testing.T) {
	src := "Before\n\n```r\nx <- 1:10\nplot(x)\n```\n\n```python\nprint(1)\n```"
	out, err := renderMarkdown(src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `<pre><code class="language-r">`) {
		t.Fatalf("r fenced block missing language class:\n%s", out)
	}
	if !strings.Contains(out, `<div class="code-label">R code</div>`) {
		t.Fatalf("r fenced block missing R-code label:\n%s", out)
	}
	if !strings.Contains(out, `<pre><code class="language-python">`) {
		t.Fatalf("python fenced block missing language class:\n%s", out)
	}
	if strings.Contains(out, "language-python") && strings.Contains(out, "R code") && strings.Count(out, "R code") != 1 {
		t.Fatalf("R-code label leaked onto a non-r block:\n%s", out)
	}
}

func TestRenderMarkdownNeverPassesRawHTML(t *testing.T) {
	src := "Hello <script>alert(1)</script> world\n\n<img src=x onerror=alert(1)>\n\n<a href=\"javascript:alert(1)\">click</a>"
	out, err := renderMarkdown(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"<script>", "<img ", "javascript:", "onerror="} {
		if strings.Contains(out, bad) {
			t.Fatalf("dangerous content passed through (%q):\n%s", bad, out)
		}
	}
}

func TestRenderMarkdownBasic(t *testing.T) {
	out, err := renderMarkdown("**bold** and `code` and a [link](https://example.com)\n\nSecond para.")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<strong>bold</strong>", "<code>code</code>", `href="https://example.com"`, "<p>Second para."} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in output:\n%s", want, out)
		}
	}
}
