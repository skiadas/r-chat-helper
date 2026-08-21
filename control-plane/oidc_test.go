package controlplane

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/golang-jwt/jwt/v5"
)

// fakeIdP is a minimal OIDC provider for exercising the client flow.
type fakeIdP struct {
	srv          *httptest.Server
	key          *rsa.PrivateKey
	clientID     string
	emailToSign  string // email stamped into the issued ID token
	lastVerifier string
}

func newFakeIdP(t *testing.T, clientID string) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idp := &fakeIdP{key: key, clientID: clientID, emailToSign: "alice@college.edu"}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                idp.srv.URL,
			"authorization_endpoint":                idp.srv.URL + "/auth",
			"token_endpoint":                        idp.srv.URL + "/token",
			"jwks_uri":                              idp.srv.URL + "/jwks",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
			"code_challenge_methods_supported":      []string{"S256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		set := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
			{Key: &idp.key.PublicKey, KeyID: "kid1", Algorithm: "RS256", Use: "sig"},
		}}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(set); err != nil {
			t.Errorf("jwks encode: %v", err)
		}
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		idp.lastVerifier = r.FormValue("code_verifier")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at",
			"id_token":     idp.signIDToken(t, idp.emailToSign),
			"token_type":   "Bearer",
		})
	})

	idp.srv = httptest.NewServer(mux)
	t.Cleanup(idp.srv.Close)
	return idp
}

func (p *fakeIdP) signIDToken(t *testing.T, email string) string {
	t.Helper()
	now := time.Now()
	c := jwt.MapClaims{
		"iss":            p.srv.URL,
		"aud":            p.clientID,
		"sub":            "user-1",
		"email":          email,
		"email_verified": true,
		"nonce":          "n1",
		"iat":            now.Unix(),
		"exp":            now.Add(time.Hour).Unix(),
	}
	s, err := jwt.NewWithClaims(jwt.SigningMethodRS256, c).SignedString(p.key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func newOIDCApp(t *testing.T, idp *fakeIdP, admins ...string) *App {
	t.Helper()
	cfg := DefaultConfig()
	cfg.DBPath = t.TempDir() + "/test.db"
	cfg.OIDCIssuer = idp.srv.URL
	cfg.OIDCClientID = idp.clientID
	cfg.OIDCClientSecret = "secret"
	cfg.OIDCRedirectURI = idp.srv.URL + "/auth/callback"
	cfg.PublicURL = "http://localhost:8090"
	cfg.CookieSecure = false
	cfg.AdminEmails = admins
	app, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.Close() })
	return app
}

// callbackReq builds a request through /auth/callback with a matching state
// cookie for the given (state, verifier, nonce).
func callbackReq(state, verifier, nonce string) *http.Request {
	b, _ := json.Marshal(oidcState{State: state, Verifier: verifier, Nonce: nonce})
	req := httptest.NewRequest(http.MethodGet,
		"/auth/callback?state="+state+"&code=the-code", nil)
	req.AddCookie(&http.Cookie{Name: oidcStateCookie,
		Value: base64.RawURLEncoding.EncodeToString(b)})
	return req
}

func TestOIDCLoginRedirectsWithPKCE(t *testing.T) {
	idp := newFakeIdP(t, "r-chat-helper")
	app := newOIDCApp(t, idp)

	rec := httptest.NewRecorder()
	app.handleOIDCLogin(rec, httptest.NewRequest(http.MethodGet, "/auth/login", nil))

	if rec.Code != http.StatusFound {
		t.Fatalf("login status = %d", rec.Code)
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if loc.Host != idp.srv.Listener.Addr().String() {
		t.Fatalf("redirect host = %q, want the SSO", loc.Host)
	}
	q := loc.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		t.Fatalf("missing PKCE params in redirect: %v", q)
	}
	if q.Get("nonce") == "" || q.Get("state") == "" {
		t.Fatal("missing nonce/state in redirect")
	}
	var got bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == oidcStateCookie && c.HttpOnly {
			got = true
		}
	}
	if !got {
		t.Fatal("expected httpOnly oidc_state cookie")
	}
}

func TestOIDCCallbackHappyPath(t *testing.T) {
	idp := newFakeIdP(t, "r-chat-helper")
	app := newOIDCApp(t, idp)
	if err := app.AddStudent(t.Context(), "alice", "alice@college.edu", "Alice", 0); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	app.handleOIDCCallback(rec, callbackReq("abc", "v1", "n1"))

	if rec.Code != http.StatusFound {
		t.Fatalf("callback status = %d body %s", rec.Code, rec.Body.String())
	}
	if idp.lastVerifier != "v1" {
		t.Fatalf("verifier sent = %q, want v1", idp.lastVerifier)
	}
	var sessionSet bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName && c.HttpOnly && c.Value != "" {
			sessionSet = true
		}
	}
	if !sessionSet {
		t.Fatal("expected session cookie to be set")
	}
}

func TestOIDCCallbackRejectsUnenrolled(t *testing.T) {
	idp := newFakeIdP(t, "r-chat-helper")
	app := newOIDCApp(t, idp)

	rec := httptest.NewRecorder()
	app.handleOIDCCallback(rec, callbackReq("abc", "v1", "n1"))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for unenrolled email", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "not enrolled") {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

func TestOIDCCallbackRejectsNonceMismatch(t *testing.T) {
	idp := newFakeIdP(t, "r-chat-helper")
	app := newOIDCApp(t, idp)
	if err := app.AddStudent(t.Context(), "alice", "alice@college.edu", "Alice", 0); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	app.handleOIDCCallback(rec, callbackReq("abc", "v1", "WRONG"))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 on nonce mismatch", rec.Code)
	}
}

func TestOIDCCallbackAdminRole(t *testing.T) {
	idp := newFakeIdP(t, "r-chat-helper")
	idp.emailToSign = "professor@college.edu"
	app := newOIDCApp(t, idp, "professor@college.edu")

	rec := httptest.NewRecorder()
	app.handleOIDCCallback(rec, callbackReq("abc", "v1", "n1"))

	if rec.Code != http.StatusFound {
		t.Fatalf("callback status = %d body %s", rec.Code, rec.Body.String())
	}
	var token string
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName && c.Value != "" {
			token = c.Value
		}
	}
	if token == "" {
		t.Fatal("expected session cookie")
	}
	claims, err := app.parseToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Role != RoleAdmin || claims.Email != "professor@college.edu" {
		t.Fatalf("claims = %+v, want admin professor", claims)
	}
}
