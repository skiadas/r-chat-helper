package controlplane

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

var (
	errInvalidURL = errors.New("invalid or unsupported URL")
	errNotAllowed = errors.New("URL not on the allowed list")
)

func errUpstreamStatus(status string) error { return fmt.Errorf("upstream %s", status) }

// webFetcher implements the webfetch tool: a server-side HTTP GET that returns
// the page's text content back to the model. HTML pages are converted to
// readable text (boilerplate stripped). It is bounded by a timeout, a maximum
// body size, and a maximum returned text size, and can be restricted to an
// allowlist of host suffixes.
type webFetcher struct {
	httpc    *http.Client
	maxBytes int64
	maxText  int64
	allow    []string // host suffix allowlist; empty = allow any
}

func newWebFetcher(cfg *Config) *webFetcher {
	return &webFetcher{
		httpc:    &http.Client{Timeout: cfg.WebFetchTimeout},
		maxBytes: cfg.WebFetchMaxBytes,
		maxText:  cfg.WebFetchMaxText,
		allow:    cfg.WebFetchAllowlist,
	}
}

// allowedHost reports whether the host is permitted by the allowlist.
func (w *webFetcher) allowedHost(host string) bool {
	host = strings.ToLower(host)
	for _, suffix := range w.allow {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	return false
}

// Fetch retrieves a URL and returns readable text: HTML pages are parsed and
// reduced to their text content, with navigation/script/style boilerplate
// removed; everything else is returned raw. The result is capped at w.maxText.
func (w *webFetcher) Fetch(ctx context.Context, rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errInvalidURL
	}
	if len(w.allow) > 0 && !w.allowedHost(u.Hostname()) {
		return "", errNotAllowed
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "r-chat-helper (course tutor); contact: instructor")
	resp, err := w.httpc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", errUpstreamStatus(resp.Status)
	}
	if w.maxText <= 0 {
		// No text cap configured; still respect the network body cap.
		body, err := io.ReadAll(io.LimitReader(resp.Body, w.maxBytes))
		if err != nil {
			return "", err
		}
		return string(body), nil
	}
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/html") {
		return htmlToText(resp.Body, w.maxText)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, w.maxBytes))
	if err != nil {
		return "", err
	}
	return truncateText(string(body), w.maxText), nil
}

// htmlToText parses an HTML document and returns its readable text content,
// skipping navigation, scripts, styles, and other boilerplate, and capping the
// result at maxText bytes.
func htmlToText(r io.Reader, maxText int64) (string, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	extractHTMLText(doc, &b)
	return truncateText(collapseText(b.String()), maxText), nil
}

// skipHTMLTags are elements whose content is never meaningful documentation.
var skipHTMLTags = map[string]bool{
	"script": true, "style": true, "noscript": true, "template": true,
	"nav": true, "form": true, "button": true, "select": true, "option": true,
	"iframe": true, "svg": true, "canvas": true, "video": true, "audio": true,
	"math": true, "header": true, "footer": true,
}

// extractHTMLText walks the parsed tree appending visible text. Block
// boundaries become newlines so paragraphs and list items stay separated.
func extractHTMLText(n *html.Node, b *strings.Builder) {
	switch n.Type {
	case html.TextNode:
		b.WriteString(n.Data)
	case html.CommentNode, html.DoctypeNode:
		return
	case html.ElementNode:
		if skipHTMLTags[n.Data] { // suppress boilerplate subtrees entirely
			return
		}
		before, after := "", ""
		switch n.Data {
		case "br", "hr", "p", "div", "li", "tr", "blockquote", "pre",
			"h1", "h2", "h3", "h4", "h5", "h6", "table", "thead", "tbody",
			"ul", "ol", "section", "article", "main":
			before, after = "\n", "\n"
		case "td", "th":
			before, after = " ", " "
		}
		b.WriteString(before)
		linkHref := ""
		if n.Data == "a" {
			linkHref = attrOf(n, "href")
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			extractHTMLText(c, b)
		}
		if linkHref != "" {
			fmt.Fprintf(b, "[%s]", linkHref)
		}
		b.WriteString(after)
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		extractHTMLText(c, b)
	}
}

func attrOf(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// collapseText trims each line and collapses runs of whitespace/blank lines
// into single newlines, so the layout the parser preserved narrows to compact
// readable paragraphs.
func collapseText(s string) string {
	var b strings.Builder
	prevBlank := true
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			prevBlank = true
			continue
		}
		if !prevBlank {
			b.WriteByte('\n')
		} else if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
		prevBlank = false
	}
	return b.String()
}

func truncateText(s string, max int64) string {
	if int64(len(s)) <= max {
		return s
	}
	return strings.TrimSpace(s[:max]) + "\n… [truncated]"
}
