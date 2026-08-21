package controlplane

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
