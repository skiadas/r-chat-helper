package controlplane

import (
	"bytes"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/util"
)

// renderMarkdown converts markdown to safe HTML for the chat UI. Raw HTML in
// the source is never passed through (goldmark renders it as a comment), so
// model or fetched content cannot inject scripts.
func renderMarkdown(src string) (string, error) {
	md := goldmark.New(
		goldmark.WithRenderer(renderer.NewRenderer(
			renderer.WithNodeRenderers(
				util.Prioritized(html.NewRenderer(), 100),
				util.Prioritized(&codeBlockRenderer{}, 1),
			),
		)),
	)
	var buf bytes.Buffer
	if err := md.Convert([]byte(src), &buf); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// codeBlockRenderer emits <pre><code class="language-X"> for fenced blocks and
// prepends a visible "R code" label when the block is tagged `r`, so students
// can tell at a glance that a snippet is R.
type codeBlockRenderer struct{}

func (r *codeBlockRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindFencedCodeBlock, r.renderFenced)
	reg.Register(ast.KindCodeBlock, r.renderIndented)
}

func (r *codeBlockRenderer) renderFenced(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	node := n.(*ast.FencedCodeBlock)
	if !entering {
		_, _ = w.WriteString("</code></pre>\n")
		return ast.WalkContinue, nil
	}
	lang := string(node.Language(source))
	if strings.EqualFold(lang, "r") {
		_, _ = w.WriteString(`<div class="code-label">R code</div>`)
	}
	_, _ = w.WriteString(`<pre><code`)
	if lang != "" {
		_, _ = w.WriteString(` class="language-`)
		_, _ = w.WriteString(lang)
		_, _ = w.WriteString(`"`)
	}
	_ = w.WriteByte('>')
	r.writeLines(w, source, &node.BaseBlock)
	return ast.WalkContinue, nil
}

func (r *codeBlockRenderer) renderIndented(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		_, _ = w.WriteString("<pre><code>")
		r.writeLines(w, source, &n.(*ast.CodeBlock).BaseBlock)
	} else {
		_, _ = w.WriteString("</code></pre>\n")
	}
	return ast.WalkContinue, nil
}

func (r *codeBlockRenderer) writeLines(w util.BufWriter, source []byte, b *ast.BaseBlock) {
	for i := 0; i < b.Lines().Len(); i++ {
		line := b.Lines().At(i)
		_, _ = w.Write(line.Value(source))
	}
}