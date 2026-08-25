package controlplane

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestHandleMe(t *testing.T) {
	app := newTestApp(t)
	ctx := t.Context()
	if err := app.AddStudent(ctx, "bob", "bob@college.edu", "Bob", 0); err != nil {
		t.Fatal(err)
	}
	s, _ := app.StudentByID(ctx, "bob")
	token, err := app.issueToken(s.Email, RoleStudent, s.ID)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	app.authenticate(http.HandlerFunc(app.handleMe)).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("me status = %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"name":"Bob"`) {
		t.Fatalf("unexpected me body: %s", rec.Body.String())
	}
}

func TestSessionCookieAuth(t *testing.T) {
	app := newTestApp(t)
	ctx := t.Context()
	if err := app.AddStudent(ctx, "alice", "alice@college.edu", "Alice", 0); err != nil {
		t.Fatal(err)
	}
	s, _ := app.StudentByID(ctx, "alice")
	token, _ := app.issueToken(s.Email, RoleStudent, s.ID)

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	app.authenticate(http.HandlerFunc(app.handleMe)).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("cookie auth failed: %d %s", rec.Code, rec.Body.String())
	}
}

func TestDisabledStudentRejected(t *testing.T) {
	app := newTestApp(t)
	ctx := t.Context()
	if err := app.AddStudent(ctx, "carol", "carol@college.edu", "Carol", 0); err != nil {
		t.Fatal(err)
	}
	if err := app.SetActive(ctx, "carol@college.edu", false); err != nil {
		t.Fatal(err)
	}
	s, _ := app.StudentByEmail(ctx, "carol@college.edu")
	token, _ := app.issueToken(s.Email, RoleStudent, s.ID)

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	app.authenticate(http.HandlerFunc(app.handleMe)).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for disabled student, got %d", rec.Code)
	}
}

// tokenWithTTL mints a signed token with a custom lifetime, bypassing the fixed
// sessionTTL of issueToken so the renewal path can be exercised.
func tokenWithTTL(t *testing.T, app *App, email, role, sid string, ttl time.Duration) string {
	t.Helper()
	c := claims{
		Email:     email,
		Role:      role,
		StudentID: sid,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   email,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(ttl)),
		},
	}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(app.jwtKey)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// TestUnauthorizedIsJSONForAPIRoute pins the API failure shape: a lapsed
// session on an /api route answers 401 JSON with the auth_required code the
// client interceptor branches on.
func TestUnauthorizedIsJSONForAPIRoute(t *testing.T) {
	app := newTestApp(t)
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	rec := httptest.NewRecorder()
	app.authenticate(http.HandlerFunc(app.handleMe)).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"code":"auth_required"`) {
		t.Fatalf("body = %s, want auth_required code", rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "" {
		t.Fatalf("API route must not redirect, got %q", loc)
	}
}

// TestUnauthorizedRedirectsOnPageRoute pins the browser failure shape: a
// lapsed session heading to a gated page route is sent to /auth/login, never
// shown a raw JSON blob.
func TestUnauthorizedRedirectsOnPageRoute(t *testing.T) {
	app := newTestApp(t)
	req := httptest.NewRequest(http.MethodGet, "/back-to-admin", nil)
	rec := httptest.NewRecorder()
	app.authenticate(http.HandlerFunc(app.handleAdminScratchReturn)).ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/auth/login" {
		t.Fatalf("location = %q, want /auth/login", loc)
	}
}

// TestDisabledStudentCarriesCode checks the 403 for a deactivated account
// carries account_disabled so the UI can tell it apart from a lapsed session.
func TestDisabledStudentCarriesCode(t *testing.T) {
	app := newDevLoginApp(t, "carol@college.edu")
	if err := app.AddStudent(t.Context(), "carol", "carol@college.edu", "Carol", 0); err != nil {
		t.Fatal(err)
	}
	if err := app.SetActive(t.Context(), "carol@college.edu", false); err != nil {
		t.Fatal(err)
	}
	token := tokenWithTTL(t, app, "carol@college.edu", RoleStudent, "carol", time.Hour)

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	app.authenticate(http.HandlerFunc(app.handleMe)).ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"code":"account_disabled"`) {
		t.Fatalf("body = %s, want account_disabled code", rec.Body.String())
	}
}

// TestRenewsTokenNearExpiry covers sliding refresh: a valid request made
// within renewThreshold of expiry comes back with a fresh session cookie and
// an expires_at on the response.
func TestRenewsTokenNearExpiry(t *testing.T) {
	app := newDevLoginApp(t, "alice@college.edu")
	if err := app.AddStudent(t.Context(), "alice", "alice@college.edu", "Alice", 0); err != nil {
		t.Fatal(err)
	}
	old := tokenWithTTL(t, app, "alice@college.edu", RoleStudent, "alice", 5*time.Minute)

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: old})
	rec := httptest.NewRecorder()
	app.authenticate(http.HandlerFunc(app.handleMe)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	var renewed bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName && c.Value != "" && c.Value != old {
			renewed = true
			if c.MaxAge != int(sessionTTL/time.Second) {
				t.Fatalf("renewed cookie maxAge = %d, want %d", c.MaxAge, int(sessionTTL/time.Second))
			}
		}
	}
	if !renewed {
		t.Fatal("near-expiry token was not renewed")
	}
	if !strings.Contains(rec.Body.String(), `"expires_at":`) {
		t.Fatalf("body missing expires_at: %s", rec.Body.String())
	}
}

func TestDoesNotRenewFreshToken(t *testing.T) {
	app := newDevLoginApp(t, "alice@college.edu")
	if err := app.AddStudent(t.Context(), "alice", "alice@college.edu", "Alice", 0); err != nil {
		t.Fatal(err)
	}
	token, err := app.issueToken("alice@college.edu", RoleStudent, "alice")
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	app.authenticate(http.HandlerFunc(app.handleMe)).ServeHTTP(rec, req)

	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName {
			t.Fatalf("fresh token unexpectedly renewed: %+v", c)
		}
	}
}

// TestRenewalKeepsModeMarker verifies a marked admin near expiry gets both the
// fresh token and a re-aligned rc_mode marker, while staying in the student
// test view (marker carries no expiry through the narrow).
func TestRenewalKeepsModeMarker(t *testing.T) {
	app := newAdminApp(t)
	scratch, err := app.EnsureScratchStudent(t.Context(), "instructor@college.edu")
	if err != nil {
		t.Fatal(err)
	}
	old := tokenWithTTL(t, app, "instructor@college.edu", RoleAdmin, "", 5*time.Minute)

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: old})
	req.AddCookie(&http.Cookie{Name: modeCookieName, Value: scratch.ID})
	rec := httptest.NewRecorder()
	app.authenticate(http.HandlerFunc(app.handleMe)).ServeHTTP(rec, req)

	seenToken, seenMode := false, false
	for _, c := range rec.Result().Cookies() {
		switch c.Name {
		case sessionCookieName:
			seenToken = c.Value != "" && c.Value != old
		case modeCookieName:
			seenMode = c.MaxAge > 0
		}
	}
	if !seenToken || !seenMode {
		t.Fatalf("renewal cookies = token %v marker %v, want both refreshed", seenToken, seenMode)
	}
	if !strings.Contains(rec.Body.String(), `"scratch":true`) || !strings.Contains(rec.Body.String(), `"expires_at":`) {
		t.Fatalf("body = %s, want scratch student view with expires_at", rec.Body.String())
	}
}
