package controlplane

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// oidcState is carried in a short-lived httpOnly cookie across the login
// redirect: state guards CSRF, verifier is the PKCE secret, nonce binds the
// ID token to this login.
type oidcState struct {
	State    string `json:"state"`
	Verifier string `json:"verifier"`
	Nonce    string `json:"nonce"`
}

func (a *App) ensureOIDC() error {
	a.oidcMu.Lock()
	defer a.oidcMu.Unlock()
	if a.oidcProvider != nil {
		return nil
	}
	if a.cfg.OIDCClientSecret == "" || a.cfg.OIDCRedirectURI == "" || a.cfg.PublicURL == "" {
		return errors.New("OIDC not configured (RC_OIDC_CLIENT_SECRET, RC_OIDC_REDIRECT_URI, RC_PUBLIC_URL)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	p, err := oidc.NewProvider(ctx, a.cfg.OIDCIssuer)
	if err != nil {
		return err
	}
	a.oidcProvider = p
	a.oauth2Config = &oauth2.Config{
		ClientID:     a.cfg.OIDCClientID,
		ClientSecret: a.cfg.OIDCClientSecret,
		RedirectURL:  a.cfg.OIDCRedirectURI,
		Scopes:       []string{oidc.ScopeOpenID, "email"},
		Endpoint: oauth2.Endpoint{
			AuthURL:   p.Endpoint().AuthURL,
			TokenURL:  p.Endpoint().TokenURL,
			AuthStyle: oauth2.AuthStyleInParams, // client_secret_post
		},
	}
	return nil
}

func (a *App) handleOIDCLogin(w http.ResponseWriter, r *http.Request) {
	if err := a.ensureOIDC(); err != nil {
		log.Printf("oidc: login unavailable: %v", err)
		a.renderAuthError(w, "Sign-in is unavailable right now; try again shortly.")
		return
	}
	st := oidcState{
		State:    randToken(16),
		Verifier: randToken(48),
		Nonce:    randToken(16),
	}
	b, _ := json.Marshal(st)
	a.setOIDCStateCookie(w, base64.RawURLEncoding.EncodeToString(b))

	sum := sha256.Sum256([]byte(st.Verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	u := a.oauth2Config.AuthCodeURL(st.State,
		oauth2.SetAuthURLParam("code_challenge", challenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
		oauth2.SetAuthURLParam("nonce", st.Nonce),
	)
	http.Redirect(w, r, u, http.StatusFound)
}

func (a *App) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	if err := a.ensureOIDC(); err != nil {
		a.renderAuthError(w, "Sign-in is unavailable right now.")
		return
	}
	ck, err := r.Cookie(oidcStateCookie)
	if err != nil {
		a.renderAuthError(w, "Login session expired. Please try signing in again.")
		return
	}
	raw, decErr := base64.RawURLEncoding.DecodeString(ck.Value)
	var st oidcState
	if decErr != nil || json.Unmarshal(raw, &st) != nil {
		a.renderAuthError(w, "Login session invalid. Please try signing in again.")
		return
	}
	a.clearOIDCStateCookie(w)

	if r.URL.Query().Get("state") != st.State {
		a.renderAuthError(w, "Login state mismatch. Please try again.")
		return
	}
	if e := r.URL.Query().Get("error"); e != "" {
		a.renderAuthError(w, "SSO reported an error: "+e)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		a.renderAuthError(w, "Missing authorization code.")
		return
	}

	tok, err := a.oauth2Config.Exchange(r.Context(), code, oauth2.VerifierOption(st.Verifier))
	if err != nil {
		log.Printf("oidc: token exchange failed: %v", err)
		a.renderAuthError(w, "Token exchange with the SSO failed.")
		return
	}
	rawID, ok := tok.Extra("id_token").(string)
	if !ok || rawID == "" {
		a.renderAuthError(w, "The SSO did not return an ID token.")
		return
	}
	verifier := a.oidcProvider.Verifier(&oidc.Config{ClientID: a.cfg.OIDCClientID})
	idtok, err := verifier.Verify(r.Context(), rawID)
	if err != nil {
		log.Printf("oidc: id_token verification failed: %v", err)
		a.renderAuthError(w, "Could not verify your login.")
		return
	}
	if idtok.Nonce != st.Nonce {
		a.renderAuthError(w, "Login nonce mismatch. Please try again.")
		return
	}
	var oc struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
	}
	if err := idtok.Claims(&oc); err != nil || oc.Email == "" || !oc.EmailVerified {
		a.renderAuthError(w, "The SSO did not provide a verified email.")
		return
	}
	email := strings.ToLower(oc.Email)

	// Instructor?
	if a.adminEmails[email] {
		token, err := a.issueToken(email, RoleAdmin, "")
		if err != nil {
			a.renderAuthError(w, "Could not start a session.")
			return
		}
		a.setSessionCookie(w, token)
		http.Redirect(w, r, a.cfg.PublicURL+"/", http.StatusFound)
		return
	}

	// Student? Must be on the admin-managed allowlist (students table).
	s, err := a.StudentByEmail(r.Context(), email)
	if err != nil {
		log.Printf("oidc: student lookup for %s failed: %v", email, err)
		a.renderAuthError(w, "Could not look up your enrollment.")
		return
	}
	if s == nil || !s.Active {
		a.renderAuthError(w, "Your email is not enrolled for this class. Ask your instructor for access.")
		return
	}
	token, err := a.issueToken(email, RoleStudent, s.ID)
	if err != nil {
		a.renderAuthError(w, "Could not start a session.")
		return
	}
	a.setSessionCookie(w, token)
	http.Redirect(w, r, a.cfg.PublicURL+"/", http.StatusFound)
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	a.clearSessionCookie(w)
	http.Redirect(w, r, a.cfg.PublicURL+"/", http.StatusFound)
}

func (a *App) renderAuthError(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	w.Write([]byte("<!doctype html><html><head><title>Sign in</title></head><body style='font-family:sans-serif;padding:2rem'><h2>Sign in</h2><p></p><p>" +
		htmlEscape(msg) +
		`</p><p><a href="/auth/login">Try again</a></p></body></html>`))
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;").Replace(s)
}

func randToken(nBytes int) string {
	buf := make([]byte, nBytes)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}
