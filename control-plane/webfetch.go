package controlplane

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

var (
	errInvalidURL = errors.New("invalid or unsupported URL")
	errNotAllowed = errors.New("URL not on the allowed list")
)

func errUpstreamStatus(status string) error { return fmt.Errorf("upstream %s", status) }

// webFetcher implements the webfetch tool: a server-side HTTP GET that returns
// the page's text content back to the model. It is bounded by a timeout and a
// maximum body size, and can be restricted to an allowlist of host suffixes.
type webFetcher struct {
	httpc    *http.Client
	maxBytes int64
	allow    []string // host suffix allowlist; empty = allow any
}

func newWebFetcher(cfg *Config) *webFetcher {
	return &webFetcher{
		httpc:    &http.Client{Timeout: cfg.WebFetchTimeout},
		maxBytes: cfg.WebFetchMaxBytes,
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

// Fetch retrieves and returns the text content of a URL.
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
	body, err := io.ReadAll(io.LimitReader(resp.Body, w.maxBytes))
	if err != nil {
		return "", err
	}
	return string(body), nil
}
