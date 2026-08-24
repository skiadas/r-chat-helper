package controlplane

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type claims struct {
	Email     string `json:"email"`
	Role      string `json:"role"`
	StudentID string `json:"sid,omitempty"`
	// Scratch is set only on the *effective* claims of a marked session (see
	// authenticate); tokens never carry it. It flips the authorization scope
	// from admin to the admin's test student identity.
	Scratch bool `json:"-"`
	jwt.RegisteredClaims
}

var errNoAuth = errors.New("missing or invalid credentials")

// issueToken mints a clean identity token. The token never carries mode
// state: an instructor's test session is signaled by the rc_mode cookie, not
// by the token, so navigation between the admin and student views never
// crosses an identity change.
func (a *App) issueToken(email, role, studentID string) (string, error) {
	now := time.Now()
	c := claims{
		Email:     email,
		Role:      role,
		StudentID: studentID,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   email,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(a.cfg.sessionTTL)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(a.jwtKey)
}

func (a *App) parseToken(raw string) (*claims, error) {
	tok, err := jwt.ParseWithClaims(raw, &claims{}, func(t *jwt.Token) (any, error) {
		return a.jwtKey, nil
	})
	if err != nil || !tok.Valid {
		return nil, errNoAuth
	}
	c, ok := tok.Claims.(*claims)
	if !ok || c.Email == "" || c.Role == "" {
		return nil, errNoAuth
	}
	return c, nil
}

// claimsFromRequest reads the session cookie first, then the Authorization
// header (convenience for curl and tests).
func (a *App) claimsFromRequest(r *http.Request) (*claims, error) {
	if ck, err := r.Cookie(sessionCookieName); err == nil && ck.Value != "" {
		if c, err := a.parseToken(ck.Value); err == nil {
			return c, nil
		}
	}
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		if c, err := a.parseToken(strings.TrimPrefix(h, "Bearer ")); err == nil {
			return c, nil
		}
	}
	return nil, errNoAuth
}

// authenticate verifies the session token, resolves the effective identity
// (the token plus any rc_mode marker), and for student-role identities loads
// the student record into the request context.
func (a *App) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := a.claimsFromRequest(r)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "authentication required")
			return
		}
		c = a.effectiveClaims(w, r, c)
		ctx := context.WithValue(r.Context(), ctxKeyClaims{}, c)
		if c.Role == RoleStudent {
			s, err := a.StudentByID(r.Context(), c.StudentID)
			if err != nil || s == nil || !s.Active {
				writeErr(w, http.StatusForbidden, "account disabled")
				return
			}
			ctx = context.WithValue(ctx, ctxKeyStudent{}, s)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// effectiveClaims narrows an admin token to the admin's scratch student
// identity when the rc_mode marker is present. Privilege always comes from the
// token: the marker can only shrink scope (to a student), never widen it, and
// it is only honored when the token is an admin and the marker names that
// admin's own scratch student. A stale marker (missing student row, wrong
// owner) is cleared and ignored.
func (a *App) effectiveClaims(w http.ResponseWriter, r *http.Request, c *claims) *claims {
	if c.Role != RoleAdmin {
		return c
	}
	mode, err := r.Cookie(modeCookieName)
	if err != nil || mode.Value == "" {
		return c
	}
	s, err := a.StudentByID(r.Context(), mode.Value)
	if err != nil || s == nil || s.Email != c.Email {
		a.clearModeCookie(w)
		return c
	}
	return &claims{Email: c.Email, Role: RoleStudent, StudentID: s.ID, Scratch: true}
}

type ctxKeyClaims struct{}
type ctxKeyStudent struct{}

func claimsOf(r *http.Request) *claims {
	c, _ := r.Context().Value(ctxKeyClaims{}).(*claims)
	return c
}

func studentOf(r *http.Request) *Student {
	s, _ := r.Context().Value(ctxKeyStudent{}).(*Student)
	return s
}

// cookie helpers

func (a *App) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   a.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(a.cfg.sessionTTL / time.Second),
	})
}

func (a *App) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   a.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// setModeCookie records the student id the instructor is currently acting as
// (their scratch test identity). The marker narrows an admin token to student
// scope; it is the sole mechanism for the admin <-> student round-trip.
func (a *App) setModeCookie(w http.ResponseWriter, studentID string) {
	http.SetCookie(w, &http.Cookie{
		Name:     modeCookieName,
		Value:    studentID,
		Path:     "/",
		HttpOnly: true,
		Secure:   a.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(a.cfg.sessionTTL / time.Second),
	})
}

func (a *App) clearModeCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     modeCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   a.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func (a *App) setOIDCStateCookie(w http.ResponseWriter, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     oidcStateCookie,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   a.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(oidcStateTTL / time.Second),
	})
}

func (a *App) clearOIDCStateCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     oidcStateCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   a.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// randomSecret generates a fresh signing key for local development. Tokens do
// not survive a restart; set RC_JWT_SECRET for a stable deployment.
func randomSecret() ([]byte, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	return []byte(base64.RawURLEncoding.EncodeToString(buf)), nil
}
