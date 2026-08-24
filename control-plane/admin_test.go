package controlplane

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newAdminApp builds an app with an enrolled student and an admin email
// (via dev login) for exercising the admin surface.
func newAdminApp(t *testing.T) *App {
	t.Helper()
	app := newDevLoginApp(t, "instructor@college.edu")
	// adminEmails is built in New() from cfg.AdminEmails, so seed the map
	// directly after configuring the dev login.
	app.adminEmails["instructor@college.edu"] = true
	if err := app.AddStudent(t.Context(), "alice", "alice@college.edu", "Alice", 1_000_000); err != nil {
		t.Fatal(err)
	}
	return app
}

// adminToken signs the admin email in and returns a session cookie.
func adminToken(t *testing.T, app *App) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	app.handleOIDCLogin(rec, httptest.NewRequest(http.MethodGet, "/auth/login", nil))
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName && c.Value != "" {
			return c
		}
	}
	t.Fatal("admin login did not set a session cookie")
	return nil
}

func TestAdminStudentsEndpointSucceeds(t *testing.T) {
	app := newAdminApp(t)
	cookie := adminToken(t, app)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/students", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.authenticate(app.requireAdmin(http.HandlerFunc(app.handleAdminListStudents))).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("admin students status = %d body %s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{"Alice", "alice@college.edu"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("admin students missing %q: %s", want, rec.Body.String())
		}
	}
}

func TestAdminStudentsRejectsStudent(t *testing.T) {
	app := newDevLoginApp(t, "alice@college.edu")
	if err := app.AddStudent(t.Context(), "alice", "alice@college.edu", "Alice", 0); err != nil {
		t.Fatal(err)
	}
	// A non-admin session cookie.
	rec := httptest.NewRecorder()
	app.handleOIDCLogin(rec, httptest.NewRequest(http.MethodGet, "/auth/login", nil))
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no student session cookie")
	}

	req := httptest.NewRequest(http.MethodGet, "/api/admin/students", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	app.authenticate(app.requireAdmin(http.HandlerFunc(app.handleAdminListStudents))).ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("student access to admin endpoint = %d, want 403", w.Code)
	}
}

func TestAdminCreateStudent(t *testing.T) {
	app := newAdminApp(t)
	cookie := adminToken(t, app)

	body := `{"email":"bob@college.edu","id":"bob","name":"Bob","budget_usd":2.5}`
	req := httptest.NewRequest(http.MethodPost, "/api/admin/students", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.authenticate(app.requireAdmin(http.HandlerFunc(app.handleAdminCreateStudent))).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("create student = %d body %s", rec.Code, rec.Body.String())
	}
	s, err := app.StudentByEmail(t.Context(), "bob@college.edu")
	if err != nil || s == nil {
		t.Fatalf("created student not found: %v, %v", s, err)
	}
	if s.BudgetMicros != 2_500_000 {
		t.Fatalf("budget = %d, want 2500000", s.BudgetMicros)
	}
}

// TestAdminCreateStudentDefaultsIDToEmail covers adding a student with email,
// name and budget (the admin form): the internal id falls back to the email.
func TestAdminCreateStudentDefaultsIDToEmail(t *testing.T) {
	app := newAdminApp(t)
	cookie := adminToken(t, app)

	body := `{"email":"carol@college.edu","name":"Carol","budget_usd":1.5}`
	req := httptest.NewRequest(http.MethodPost, "/api/admin/students", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.authenticate(app.requireAdmin(http.HandlerFunc(app.handleAdminCreateStudent))).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("create student = %d body %s", rec.Code, rec.Body.String())
	}
	s, err := app.StudentByEmail(t.Context(), "carol@college.edu")
	if err != nil || s == nil {
		t.Fatalf("created student not found: %v, %v", s, err)
	}
	if s.ID != "carol@college.edu" {
		t.Fatalf("id = %q, want default of email", s.ID)
	}
}

// TestAdminCreateStudentRejectsZeroBudget verifies the required-budget rule:
// a student cannot be added without a positive budget.
func TestAdminCreateStudentRejectsZeroBudget(t *testing.T) {
	app := newAdminApp(t)
	cookie := adminToken(t, app)

	body := `{"email":"no-budget@college.edu","name":"Nobody","budget_usd":0}`
	req := httptest.NewRequest(http.MethodPost, "/api/admin/students", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.authenticate(app.requireAdmin(http.HandlerFunc(app.handleAdminCreateStudent))).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("zero-budget create = %d, want 400", rec.Code)
	}
}

func TestAdminCreateStudentRejectsScratchPrefix(t *testing.T) {
	app := newAdminApp(t)
	cookie := adminToken(t, app)

	body := `{"email":"x@college.edu","id":"scratch:foo","name":"X"}`
	req := httptest.NewRequest(http.MethodPost, "/api/admin/students", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.authenticate(app.requireAdmin(http.HandlerFunc(app.handleAdminCreateStudent))).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("scratch prefix create = %d, want 400", rec.Code)
	}
}

func TestScratchIdentityIsolated(t *testing.T) {
	app := newAdminApp(t)

	scratch, err := app.EnsureScratchStudent(t.Context(), "instructor@college.edu")
	if err != nil {
		t.Fatal(err)
	}
	if scratch.ID != "scratch:instructor@college.edu" {
		t.Fatalf("scratch id = %q", scratch.ID)
	}

	// Scratch students are excluded from the real student list.
	students, err := app.ListStudentsWithSpend(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range students {
		if s.ID == scratch.ID {
			t.Fatalf("scratch student leaked into real list: %+v", s)
		}
	}
	if len(students) != 1 { // only Alice
		t.Fatalf("students = %+v, want just alice", students)
	}

	// Scratch sessions are excluded from the admin session audit.
	ses, err := app.CreateSession(t.Context(), scratch.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	_ = ses
	sessions, err := app.ListAllSessions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sessions {
		if s.StudentID == scratch.ID {
			t.Fatalf("scratch session leaked into audit: %+v", s)
		}
	}
	if len(sessions) != 0 {
		t.Fatalf("sessions = %+v, want none", sessions)
	}

	// Scratch spend is tracked separately in the summary.
	spent, count, err := app.ScratchSummary(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || spent != 0 {
		t.Fatalf("scratch summary = %d spend, %d count; want 0/1", spent, count)
	}
}

// TestAdminSessionsIncludeStudentID verifies the sessions audit carries the
// owner's internal id, which the admin filter dropdown keys on.
func TestAdminSessionsIncludeStudentID(t *testing.T) {
	app := newAdminApp(t)
	if _, err := app.CreateSession(t.Context(), "alice", "demo"); err != nil {
		t.Fatal(err)
	}
	sessions, err := app.ListAllSessions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].StudentID != "alice" {
		t.Fatalf("sessions = %+v, want one session owned by alice", sessions)
	}
}

// TestScratchLoginSetsMarker verifies entering a test session leaves the
// (admin) token untouched and instead sets the rc_mode marker to the scratch
// identity.
func TestScratchLoginSetsMarker(t *testing.T) {
	app := newAdminApp(t)
	cookie := adminToken(t, app)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/scratch-login", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.authenticate(app.requireAdmin(http.HandlerFunc(app.handleAdminScratchLogin))).ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("scratch login = %d body %s", rec.Code, rec.Body.String())
	}
	// The admin token must survive unchanged; only the marker is added.
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName && c.Value != cookie.Value {
			t.Fatal("scratch login replaced the admin token; it should only set the marker")
		}
	}
	marker := modeCookie(t, rec)
	if marker == nil || !strings.HasPrefix(marker.Value, "scratch:") {
		t.Fatalf("scratch login marker = %+v, want scratch identity", marker)
	}
}

func modeCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == modeCookieName {
			return c
		}
	}
	return nil
}

// TestMarkerNarrowsAdminScope proves a marked admin sees the student surface:
// /api/me reports the scratch student, and admin endpoints are denied.
func TestMarkerNarrowsAdminScope(t *testing.T) {
	app := newAdminApp(t)
	cookie := adminToken(t, app)
	marker := modeCookie(t, markAdmin(t, app, cookie))

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(cookie)
	req.AddCookie(marker)
	rec := httptest.NewRecorder()
	app.authenticate(http.HandlerFunc(app.handleMe)).ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), `"role":"student"`) || !strings.Contains(rec.Body.String(), `"scratch":true`) {
		t.Fatalf("marked /api/me = %s, want scratch student view", rec.Body.String())
	}

	// Admin scope is denied while marked.
	adminReq := httptest.NewRequest(http.MethodGet, "/api/admin/students", nil)
	adminReq.AddCookie(cookie)
	adminReq.AddCookie(marker)
	adminRec := httptest.NewRecorder()
	app.authenticate(app.requireAdmin(http.HandlerFunc(app.handleAdminListStudents))).ServeHTTP(adminRec, adminReq)
	if adminRec.Code != http.StatusForbidden {
		t.Fatalf("marked admin calling /api/admin/students = %d, want 403", adminRec.Code)
	}
}

// TestScratchReturnClearsMarker verifies the round-trip: clearing the marker
// restores the admin scope without touching the token.
func TestScratchReturnClearsMarker(t *testing.T) {
	app := newAdminApp(t)
	cookie := adminToken(t, app)
	marker := modeCookie(t, markAdmin(t, app, cookie))

	req := httptest.NewRequest(http.MethodGet, "/api/admin/scratch-return", nil)
	req.AddCookie(cookie)
	req.AddCookie(marker)
	rec := httptest.NewRecorder()
	app.authenticate(http.HandlerFunc(app.handleAdminScratchReturn)).ServeHTTP(rec, req)

	if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "/admin.html") {
		t.Fatalf("scratch return = %d %q, want redirect to admin", rec.Code, rec.Header().Get("Location"))
	}
	cleared := modeCookie(t, rec)
	if cleared == nil || cleared.MaxAge != -1 {
		t.Fatalf("scratch return did not clear the marker: %+v", cleared)
	}

	// With the marker gone, the same admin token is admin again.
	me := httptest.NewRecorder()
	j := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	j.AddCookie(cookie)
	app.authenticate(http.HandlerFunc(app.handleMe)).ServeHTTP(me, j)
	if !strings.Contains(me.Body.String(), `"role":"admin"`) {
		t.Fatalf("unmarked /api/me = %s, want admin", me.Body.String())
	}
}

// TestScratchReturnClearsMarkerForStudent verifies a real student calling the
// return endpoint just clears a marker they never had; the response is the
// same redirect, and they still cannot reach admin scope.
func TestScratchReturnClearsMarkerForStudent(t *testing.T) {
	app := newDevLoginApp(t, "alice@college.edu")
	if err := app.AddStudent(t.Context(), "alice", "alice@college.edu", "Alice", 0); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	app.handleOIDCLogin(rec, httptest.NewRequest(http.MethodGet, "/auth/login", nil))
	var studentCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName {
			studentCookie = c
		}
	}
	if studentCookie == nil {
		t.Fatal("student login set no session cookie")
	}

	req := httptest.NewRequest(http.MethodGet, "/api/admin/scratch-return", nil)
	req.AddCookie(studentCookie)
	w := httptest.NewRecorder()
	app.authenticate(http.HandlerFunc(app.handleAdminScratchReturn)).ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("student calling scratch-return = %d, want the same redirect", w.Code)
	}
	// A student token can never become admin: admin endpoints remain denied.
	adminReq := httptest.NewRequest(http.MethodGet, "/api/admin/students", nil)
	adminReq.AddCookie(studentCookie)
	adminRec := httptest.NewRecorder()
	app.authenticate(app.requireAdmin(http.HandlerFunc(app.handleAdminListStudents))).ServeHTTP(adminRec, adminReq)
	if adminRec.Code != http.StatusForbidden {
		t.Fatalf("student after scratch-return = %d, want 403 on admin", adminRec.Code)
	}
}

// markAdmin performs the scratch-login and returns the recorder so the caller
// can read the rc_mode cookie it set.
func markAdmin(t *testing.T, app *App, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/scratch-login", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.authenticate(app.requireAdmin(http.HandlerFunc(app.handleAdminScratchLogin))).ServeHTTP(rec, req)
	return rec
}
