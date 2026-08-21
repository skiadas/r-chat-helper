package controlplane

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds control-plane configuration, sourced from the environment.
type Config struct {
	Addr         string // HTTP listen address (default ":8080")
	DBPath       string // SQLite database path
	JWTSecret    []byte // token signing secret; ephemeral if empty
	ModelsDevURL string // rate catalog source
	Upstream     string // chat upstream base URL; default is the opencode Go rail
	ProviderKey  string // shared class API key, injected as the Bearer token on every upstream call
	LocksModel   string // model id locked for all requests

	// WebFetch limits.
	WebFetchEnabled   bool
	WebFetchTimeout   time.Duration
	WebFetchMaxBytes  int64
	WebFetchMaxText   int64
	WebFetchAllowlist []string // empty = any URL (best-effort)

	// OIDC (authentication via the external SSO).
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCRedirectURI  string
	PublicURL        string
	CookieSecure     bool
	AdminEmails      []string
	DevEmail         string // dev only: auto sign-in identity while OIDC is unconfigured

	sessionTTL time.Duration
}

const (
	sessionCookieName = "rc_session"
	oidcStateCookie   = "oidc_state"
	sessionTTL        = 12 * time.Hour
	oidcStateTTL      = 10 * time.Minute

	RoleStudent = "student"
	RoleAdmin   = "admin"

	// LockedModelID is the only model students may use; enforced on every
	// upstream request by the client.
	LockedModelID = "deepseek-v4-flash"
)

func DefaultConfig() *Config {
	return &Config{
		Addr:         envOr("RC_ADDR", ":8080"),
		DBPath:       envOr("RC_DB", "r-chat-helper.db"),
		JWTSecret:    []byte(envOr("RC_JWT_SECRET", "")),
		ModelsDevURL: envOr("RC_MODELS_URL", "https://models.dev/api.json"),
		Upstream:     envOr("RC_UPSTREAM", "https://opencode.ai/zen/go/v1"),
		ProviderKey:  envOr("RC_PROVIDER_KEY", ""),
		LocksModel:   envOr("RC_MODEL", LockedModelID),

		WebFetchEnabled:   envBool("RC_WEBFETCH", true),
		WebFetchTimeout:   15 * time.Second,
		WebFetchMaxBytes:  512 << 10,
		WebFetchMaxText:   32 << 10,
		WebFetchAllowlist: splitList(envOr("RC_WEBFETCH_ALLOW", "")),

		OIDCIssuer:       envOr("RC_OIDC_ISSUER", "https://sso.harisskiadas.com"),
		OIDCClientID:     envOr("RC_OIDC_CLIENT_ID", "r-chat-helper"),
		OIDCClientSecret: envOr("RC_OIDC_CLIENT_SECRET", ""),
		OIDCRedirectURI:  envOr("RC_OIDC_REDIRECT_URI", ""),
		PublicURL:        strings.TrimRight(envOr("RC_PUBLIC_URL", ""), "/"),
		CookieSecure:     envBool("RC_COOKIE_SECURE", true),
		AdminEmails:      splitList(envOr("RC_ADMIN_EMAILS", "")),
		DevEmail:         envOr("RC_DEV_EMAIL", ""),

		sessionTTL: sessionTTL,
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

// splitList splits a ";" or "," separated list, lowercased, empties removed.
func splitList(s string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ';' || r == ',' }) {
		if part = strings.ToLower(strings.TrimSpace(part)); part != "" {
			out = append(out, part)
		}
	}
	return out
}
