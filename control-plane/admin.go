package controlplane

import (
	"net/http"
	"strings"
)

// requireAdmin gates a handler to instructors (the admin role issued on
// sign-in for RC_ADMIN_EMAILS).
func (a *App) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := claimsOf(r)
		if c == nil || c.Role != RoleAdmin {
			writeErr(w, http.StatusForbidden, "instructor access required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) handleAdminListStudents(w http.ResponseWriter, r *http.Request) {
	c := claimsOf(r)
	students, err := a.ListStudentsWithSpend(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to list students")
		return
	}
	out := make([]map[string]any, 0, len(students)+1)
	for _, sw := range students {
		out = append(out, map[string]any{
			"id":            sw.ID,
			"email":         sw.Email,
			"name":          sw.Name,
			"active":        sw.Active,
			"budget_usd":    float64(sw.BudgetMicros) / 1e6,
			"spent_usd":     float64(sw.SpentMicros) / 1e6,
			"remaining_usd": float64(sw.BudgetMicros-sw.SpentMicros) / 1e6,
		})
	}
	// The current admin's scratch test identity appears as a tagged row so its
	// budget is visible and editable like any student's.
	scratch, err := a.EnsureScratchStudent(r.Context(), c.Email)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to load test identity")
		return
	}
	spent, err := a.SpendByStudent(r.Context(), scratch.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to load usage")
		return
	}
	out = append(out, map[string]any{
		"id":            scratch.ID,
		"email":         scratch.Email,
		"name":          scratch.Name,
		"active":        scratch.Active,
		"test":          true,
		"budget_usd":    float64(scratch.BudgetMicros) / 1e6,
		"spent_usd":     float64(spent) / 1e6,
		"remaining_usd": float64(scratch.BudgetMicros-spent) / 1e6,
	})
	writeJSON(w, http.StatusOK, map[string]any{"students": out})
}

func (a *App) handleAdminCreateStudent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email     string  `json:"email"`
		ID        string  `json:"id"`
		Name      string  `json:"name"`
		BudgetUsd float64 `json:"budget_usd"`
	}
	if err := readJSON(r, &req); err != nil || req.Email == "" || req.Name == "" {
		writeErr(w, http.StatusBadRequest, "email and name are required")
		return
	}
	if req.BudgetUsd <= 0 {
		writeErr(w, http.StatusBadRequest, "a budget greater than zero is required")
		return
	}
	// Students are tracked by email; the internal id defaults to it so the
	// admin never has to invent one.
	if req.ID == "" {
		req.ID = req.Email
	}
	if strings.HasPrefix(req.ID, "scratch:") {
		writeErr(w, http.StatusBadRequest, "reserved id prefix")
		return
	}
	if err := a.AddStudent(r.Context(), req.ID, req.Email, req.Name, micros(req.BudgetUsd)); err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to add student")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": req.ID})
}

func (a *App) handleAdminUpdateStudent(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("id")
	s, err := a.StudentByID(r.Context(), sid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to load student")
		return
	}
	if s == nil {
		writeErr(w, http.StatusNotFound, "student not found")
		return
	}
	var req struct {
		Active    *bool    `json:"active"`
		BudgetUsd *float64 `json:"budget_usd"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Active != nil {
		if err := a.SetActive(r.Context(), s.Email, *req.Active); err != nil {
			writeErr(w, http.StatusInternalServerError, "failed to update student")
			return
		}
	}
	if req.BudgetUsd != nil {
		if *req.BudgetUsd <= 0 {
			writeErr(w, http.StatusBadRequest, "a budget greater than zero is required")
			return
		}
		if err := a.SetBudget(r.Context(), sid, micros(*req.BudgetUsd)); err != nil {
			writeErr(w, http.StatusInternalServerError, "failed to update budget")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": sid})
}

func (a *App) handleAdminListSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := a.ListAllSessions(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to list sessions")
		return
	}
	out := make([]map[string]any, 0, len(sessions))
	for _, as := range sessions {
		out = append(out, map[string]any{
			"id":            as.ID,
			"title":         as.Title,
			"committed":     as.Committed,
			"deleted":       as.Deleted,
			"created":       as.CreatedAt,
			"updated":       as.UpdatedAt,
			"student_id":    as.StudentID,
			"student_email": as.StudentEmail,
			"student_name":  as.StudentName,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *App) handleAdminSessionMessages(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("id")
	msgs, err := a.Messages(r.Context(), sid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to load messages")
		return
	}
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, messagePayload(m))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAdminScratchLogin drops the instructor into their scratch test
// identity by setting the rc_mode marker (the token is untouched). Spend
// under the scratch identity is tracked separately in the admin view.
func (a *App) handleAdminScratchLogin(w http.ResponseWriter, r *http.Request) {
	c := claimsOf(r)
	s, err := a.EnsureScratchStudent(r.Context(), c.Email)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to create test identity")
		return
	}
	a.setModeCookie(w, s.ID)
	http.Redirect(w, r, a.cfg.PublicURL+"/", http.StatusFound)
}

// handleAdminScratchReturn is the round-trip back out of a test session: it
// clears the rc_mode marker so the (still-present) admin token regains its
// full scope and the instructor lands on the dashboard. Because the token
// never changed, no privilege is minted here — the marker can only narrow,
// so clearing it is always safe.
func (a *App) handleAdminScratchReturn(w http.ResponseWriter, r *http.Request) {
	a.clearModeCookie(w)
	http.Redirect(w, r, a.cfg.PublicURL+"/admin.html", http.StatusFound)
}

// micros converts a dollar amount to integer micro-dollars.
func micros(usd float64) int64 {
	return int64(usd * 1e6)
}
