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

// TestAdminCreateStudentDefaultsIDToEmail covers adding a student with only
// email+name (the admin form): the internal id falls back to the email.
func TestAdminCreateStudentDefaultsIDToEmail(t *testing.T) {
	app := newAdminApp(t)
	cookie := adminToken(t, app)

	body := `{"email":"carol@college.edu","name":"Carol"}`
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

func TestScratchLoginIssuesScratchClaim(t *testing.T) {
	app := newAdminApp(t)
	cookie := adminToken(t, app)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/scratch-login", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.authenticate(app.requireAdmin(http.HandlerFunc(app.handleAdminScratchLogin))).ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("scratch login = %d body %s", rec.Code, rec.Body.String())
	}
	var scratchCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName {
			scratchCookie = c
		}
	}
	if scratchCookie == nil {
		t.Fatal("scratch login set no session cookie")
	}
	claims, err := app.parseToken(scratchCookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	if !claims.Scratch || claims.Role != RoleStudent || claims.StudentID != "scratch:instructor@college.edu" {
		t.Fatalf("scratch claims = %+v", claims)
	}
}

// TestScratchReturnReissuesAdmin verifies the scratch round-trip: holding a
// scratch (student) token, the return endpoint mints an admin token and
// redirects to the dashboard.
func TestScratchReturnReissuesAdmin(t *testing.T) {
	app := newAdminApp(t)

	// Obtain a scratch token via scratch-login.
	scratch := scratchToken(t, app)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/scratch-return", nil)
	req.AddCookie(scratch)
	rec := httptest.NewRecorder()
	app.authenticate(http.HandlerFunc(app.handleAdminScratchReturn)).ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("scratch return = %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Header().Get("Location"), "/admin.html") {
		t.Fatalf("redirect location = %q, want /admin.html", rec.Header().Get("Location"))
	}
	var adminCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName {
			adminCookie = c
		}
	}
	if adminCookie == nil {
		t.Fatal("scratch return set no session cookie")
	}
	claims, err := app.parseToken(adminCookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Role != RoleAdmin || claims.Scratch {
		t.Fatalf("return claims = %+v, want clean admin", claims)
	}
	if claims.Email != "instructor@college.edu" {
		t.Fatalf("return claims email = %q", claims.Email)
	}
}

// TestScratchReturnRejectsRealStudent verifies a real student token cannot
// mint an admin token via the return endpoint.
func TestScratchReturnRejectsRealStudent(t *testing.T) {
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

	if w.Code != http.StatusForbidden {
		t.Fatalf("student calling scratch-return = %d, want 403", w.Code)
	}
}

// scratchToken signs the instructor into the scratch test identity and
// returns the resulting session cookie.
func scratchToken(t *testing.T, app *App) *http.Cookie {
	t.Helper()
	adminRec := httptest.NewRecorder()
	app.handleOIDCLogin(adminRec, httptest.NewRequest(http.MethodGet, "/auth/login", nil))
	var adminCookie *http.Cookie
	for _, c := range adminRec.Result().Cookies() {
		if c.Name == sessionCookieName {
			adminCookie = c
		}
	}
	if adminCookie == nil {
		t.Fatal("admin login set no session cookie")
	}

	req := httptest.NewRequest(http.MethodGet, "/api/admin/scratch-login", nil)
	req.AddCookie(adminCookie)
	rec := httptest.NewRecorder()
	app.authenticate(app.requireAdmin(http.HandlerFunc(app.handleAdminScratchLogin))).ServeHTTP(rec, req)

	var scratchCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName {
			scratchCookie = c
		}
	}
	if scratchCookie == nil {
		t.Fatal("scratch login set no session cookie")
	}
	return scratchCookie
}
