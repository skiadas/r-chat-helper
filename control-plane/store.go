package controlplane

import (
	"context"
	"database/sql"
	"time"
)

// Student is a per-student account. Presence in this table with active=1 is
// the admin-managed allowlist: only these emails may sign in.
type Student struct {
	ID           string
	Email        string
	Name         string
	BudgetMicros int64
	Active       bool
	CreatedAt    int64
}

// Tokens is a cumulative per-session token total as reported by the upstream.
type Tokens struct {
	Input     int64
	Output    int64
	CacheRead int64
}

// Rate is list-price model cost in micro-dollars per 1M tokens.
type Rate struct {
	Provider     string
	Model        string
	Multiplier   float64
	InputMicros  int64
	OutputMicros int64
	CacheMicros  int64
	FetchedAt    int64
}

// UsageEvent is a frozen per-interaction cost.
type UsageEvent struct {
	StudentID       string
	SessionID       string
	Model           string
	RecordedAt      int64
	InputTokens     int64
	OutputTokens    int64
	CacheReadTokens int64
	CostMicros      int64
}

// Sessions begin as drafts (committed=false) and only
// become visible in the sidebar once committed. Soft-deleted sessions keep
// their row for admin review but never appear to students. Summary, when set,
// is carried into the model-facing context of a later session started from
// this one; HasSummary marks a session created from a summary (it may never
// generate another, so a carried summary can only be composed once).
// LastPromptTokens is the model context size of the most recently completed
// turn, the basis of the session size cap.
type Session struct {
	ID               string
	StudentID        string
	Title            string
	Committed        bool
	Deleted          bool
	CreatedAt        int64
	UpdatedAt        int64
	Summary          string
	HasSummary       bool
	LastPromptTokens int64
}

// Message is a single persisted chat turn. Context, when set, is the
// model-facing content (e.g. an assistant turn with fetched tool output
// inlined); Text is what the student sees.
type Message struct {
	ID        int64
	SessionID string
	Role      string
	Text      string
	Context   string
	Seq       int64
}

func (a *App) AddStudent(ctx context.Context, id, email, name string, budgetMicros int64) error {
	_, err := a.db.ExecContext(ctx,
		`INSERT INTO students(id, email, name, budget_micros, active, created_at) VALUES(?,?,?,?,1,?)`,
		id, email, name, budgetMicros, time.Now().Unix())
	return err
}

func (a *App) scanStudent(row *sql.Row) (*Student, error) {
	s := &Student{}
	var active int
	err := row.Scan(&s.ID, &s.Email, &s.Name, &s.BudgetMicros, &active, &s.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.Active = active != 0
	return s, nil
}

func (a *App) StudentByID(ctx context.Context, id string) (*Student, error) {
	return a.scanStudent(a.db.QueryRowContext(ctx,
		`SELECT id, email, name, budget_micros, active, created_at FROM students WHERE id=?`, id))
}

func (a *App) StudentByEmail(ctx context.Context, email string) (*Student, error) {
	return a.scanStudent(a.db.QueryRowContext(ctx,
		`SELECT id, email, name, budget_micros, active, created_at FROM students WHERE email=?`,
		email))
}

func (a *App) ListStudents(ctx context.Context) ([]Student, error) {
	rows, err := a.db.QueryContext(ctx,
		`SELECT id, email, name, budget_micros, active, created_at FROM students ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Student
	for rows.Next() {
		s := Student{}
		var active int
		if err := rows.Scan(&s.ID, &s.Email, &s.Name, &s.BudgetMicros, &active, &s.CreatedAt); err != nil {
			return nil, err
		}
		s.Active = active != 0
		out = append(out, s)
	}
	return out, rows.Err()
}

func (a *App) SetBudget(ctx context.Context, studentID string, budgetMicros int64) error {
	_, err := a.db.ExecContext(ctx, `UPDATE students SET budget_micros=? WHERE id=?`, budgetMicros, studentID)
	return err
}

// SetActive toggles whether a student email may sign in.
func (a *App) SetActive(ctx context.Context, email string, active bool) error {
	v := 0
	if active {
		v = 1
	}
	_, err := a.db.ExecContext(ctx, `UPDATE students SET active=? WHERE email=?`, v, email)
	return err
}

// StudentWithSpend is a student joined with their total frozen cost.
type StudentWithSpend struct {
	Student
	SpentMicros int64
}

// ListStudentsWithSpend returns all students (except scratch identities) with
// their total spend, for the admin surface.
func (a *App) ListStudentsWithSpend(ctx context.Context) ([]StudentWithSpend, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT st.id, st.email, st.name, st.budget_micros, st.active, st.created_at,
		       COALESCE((SELECT SUM(cost_micros) FROM usage_events u WHERE u.student_id = st.id), 0)
		FROM students st
		WHERE st.id NOT LIKE 'scratch:%'
		ORDER BY st.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StudentWithSpend
	for rows.Next() {
		sw := StudentWithSpend{}
		var active int
		if err := rows.Scan(&sw.ID, &sw.Email, &sw.Name, &sw.BudgetMicros, &active, &sw.CreatedAt, &sw.SpentMicros); err != nil {
			return nil, err
		}
		sw.Active = active != 0
		out = append(out, sw)
	}
	return out, rows.Err()
}

// ScratchSummary returns the total spend across all scratch (instructor test)
// identities, so the admin view can surface testing cost separately.
func (a *App) ScratchSummary(ctx context.Context) (totalMicros int64, count int, err error) {
	err = a.db.QueryRowContext(ctx, `
		SELECT COALESCE((SELECT SUM(cost_micros) FROM usage_events u
		                  JOIN students st ON st.id = u.student_id
		                 WHERE st.id LIKE 'scratch:%'), 0),
		       (SELECT COUNT(*) FROM students WHERE id LIKE 'scratch:%')`).Scan(&totalMicros, &count)
	return
}

// AdminSession is a session joined with its owner, for the admin surface.
type AdminSession struct {
	Session
	StudentEmail string
	StudentName  string
}

// ListAllSessions returns every non-scratch session (drafts and soft-deleted
// included), most recently active first, with the owner's identity.
func (a *App) ListAllSessions(ctx context.Context) ([]AdminSession, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT s.id, s.student_id, s.title, s.committed, COALESCE(s.deleted_at,0), s.created_at, s.updated_at,
		       COALESCE(s.summary,''), s.has_summary, s.last_prompt_tokens,
		       st.email, st.name
		FROM sessions s JOIN students st ON st.id = s.student_id
		WHERE st.id NOT LIKE 'scratch:%'
		ORDER BY s.updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AdminSession
	for rows.Next() {
		as := AdminSession{}
		var committed, deleted, hasSummary int
		if err := rows.Scan(&as.ID, &as.StudentID, &as.Title, &committed, &deleted, &as.CreatedAt, &as.UpdatedAt,
			&as.Summary, &hasSummary, &as.LastPromptTokens,
			&as.StudentEmail, &as.StudentName); err != nil {
			return nil, err
		}
		as.Committed = committed != 0
		as.Deleted = deleted != 0
		as.HasSummary = hasSummary != 0
		out = append(out, as)
	}
	return out, rows.Err()
}

// EnsureScratchStudent returns the scratch (instructor test) identity for the
// given admin email, creating it on first use. Scratch identities appear in
// neither student lists nor session audits, and their spend is tracked
// separately so instructor testing never touches a student budget.
func (a *App) EnsureScratchStudent(ctx context.Context, adminEmail string) (*Student, error) {
	id := "scratch:" + adminEmail
	s, err := a.StudentByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if s != nil {
		return s, nil
	}
	// The scratch identity keeps a small fixed budget so the instructor's
	// test flow exercises real cap enforcement; it is editable in the admin
	// view like any other student row.
	const scratchBudgetMicros = 1_000_000 // $1.00
	if err := a.AddStudent(ctx, id, adminEmail, "Instructor (scratch)", scratchBudgetMicros); err != nil {
		return nil, err
	}
	return a.StudentByID(ctx, id)
}

// --- sessions ---

func (a *App) CreateSession(ctx context.Context, studentID, title string) (*Session, error) {
	id := newID()
	now := time.Now().Unix()
	_, err := a.db.ExecContext(ctx,
		`INSERT INTO sessions(id, student_id, title, committed, created_at, updated_at) VALUES(?,?,?,0,?,?)`,
		id, studentID, title, now, now)
	if err != nil {
		return nil, err
	}
	return &Session{ID: id, StudentID: studentID, Title: title, CreatedAt: now, UpdatedAt: now}, nil
}

// CreateSessionWithSummary creates a session whose model-facing context starts
// from a carry-over summary of another session. HasSummary is set so the
// session can never in turn be summarized (a summary composes at most once).
func (a *App) CreateSessionWithSummary(ctx context.Context, studentID, title, summary string) (*Session, error) {
	id := newID()
	now := time.Now().Unix()
	_, err := a.db.ExecContext(ctx,
		`INSERT INTO sessions(id, student_id, title, committed, summary, has_summary, created_at, updated_at)
		 VALUES(?,?,?,0,?,1,?,?)`,
		id, studentID, title, summary, now, now)
	if err != nil {
		return nil, err
	}
	return &Session{ID: id, StudentID: studentID, Title: title, Summary: summary, HasSummary: true, CreatedAt: now, UpdatedAt: now}, nil
}

// SetSessionPromptTokens records the model context size (in tokens) of the
// session's most recently completed turn; the session size cap compares
// against it before the next turn is accepted.
func (a *App) SetSessionPromptTokens(ctx context.Context, sessionID string, tokens int64) error {
	_, err := a.db.ExecContext(ctx, `UPDATE sessions SET last_prompt_tokens=? WHERE id=?`, tokens, sessionID)
	return err
}

// scanSession scans a session row in the canonical column order.
func scanSession(row interface{ Scan(...any) error }) (*Session, error) {
	s := &Session{}
	var committed, deleted, hasSummary int
	err := row.Scan(&s.ID, &s.StudentID, &s.Title, &committed, &deleted, &s.CreatedAt, &s.UpdatedAt,
		&s.Summary, &hasSummary, &s.LastPromptTokens)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.Committed = committed != 0
	s.Deleted = deleted != 0
	s.HasSummary = hasSummary != 0
	return s, nil
}

func (a *App) Session(ctx context.Context, id string) (*Session, error) {
	return scanSession(a.db.QueryRowContext(ctx,
		`SELECT id, student_id, title, committed, COALESCE(deleted_at,0), created_at, updated_at,
		        COALESCE(summary,''), has_summary, last_prompt_tokens FROM sessions WHERE id=?`, id))
}

// ListSessions returns the student's committed, non-deleted sessions, most
// recently active first. Drafts and soft-deleted sessions are excluded.
func (a *App) ListSessions(ctx context.Context, studentID string) ([]Session, error) {
	rows, err := a.db.QueryContext(ctx,
		`SELECT id, student_id, title, committed, COALESCE(deleted_at,0), created_at, updated_at,
		        COALESCE(summary,''), has_summary, last_prompt_tokens FROM sessions
		 WHERE student_id=? AND committed=1 AND deleted_at IS NULL ORDER BY updated_at DESC`, studentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

// LatestSession returns the student's most recently active non-deleted session,
// draft or committed, or nil if the student has none.
func (a *App) LatestSession(ctx context.Context, studentID string) (*Session, error) {
	return scanSession(a.db.QueryRowContext(ctx,
		`SELECT id, student_id, title, committed, COALESCE(deleted_at,0), created_at, updated_at,
		        COALESCE(summary,''), has_summary, last_prompt_tokens FROM sessions
		 WHERE student_id=? AND deleted_at IS NULL ORDER BY updated_at DESC LIMIT 1`, studentID))
}

// CommitSession marks a draft as committed (visible in the sidebar).
func (a *App) CommitSession(ctx context.Context, id string) error {
	_, err := a.db.ExecContext(ctx, `UPDATE sessions SET committed=1 WHERE id=?`, id)
	return err
}

// RenameSession sets a session title.
func (a *App) RenameSession(ctx context.Context, id, title string) error {
	_, err := a.db.ExecContext(ctx, `UPDATE sessions SET title=? WHERE id=?`, title, id)
	return err
}

// DeleteSession soft-deletes a session: it disappears from the sidebar and
// rejects further messages, but the row (and its messages) remain for admins.
func (a *App) DeleteSession(ctx context.Context, id string) error {
	_, err := a.db.ExecContext(ctx, `UPDATE sessions SET deleted_at=? WHERE id=?`, time.Now().Unix(), id)
	return err
}

// AddMessage appends a turn (role: user|assistant), bumps the session's
// updated_at, and returns its persisted record. Returns an error if the
// session does not belong to the student.
// AddMessage appends a turn (role: user|assistant) and bumps the session's
// updated_at. context, when non-empty, is what the model sees in later turns
// (e.g. an assistant turn with fetched tool output inlined) while text is what
// the student's chat shows.
func (a *App) AddMessage(ctx context.Context, sessionID, role, text, context string) error {
	_, err := a.db.ExecContext(ctx,
		`INSERT INTO messages(session_id, role, text, context, seq) SELECT ?,?,?,?, COALESCE(MAX(seq),0)+1 FROM messages WHERE session_id=?`,
		sessionID, role, text, context, sessionID)
	if err != nil {
		return err
	}
	_, err = a.db.ExecContext(ctx,
		`UPDATE sessions SET updated_at=? WHERE id=?`, time.Now().Unix(), sessionID)
	return err
}

// CountMessages returns the number of messages in a session, optionally
// filtered by role.
func (a *App) CountMessages(ctx context.Context, sessionID, role string) (int, error) {
	var n int
	err := a.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM messages WHERE session_id=? AND role=?`, sessionID, role).Scan(&n)
	if err != nil {
		return 0, err
	}
	return n, nil
}

func (a *App) Messages(ctx context.Context, sessionID string) ([]Message, error) {
	rows, err := a.db.QueryContext(ctx,
		`SELECT id, session_id, role, text, COALESCE(context,''), seq FROM messages WHERE session_id=? ORDER BY seq`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		m := Message{}
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &m.Text, &m.Context, &m.Seq); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// sessionOwned verifies a session exists, is not deleted, and belongs to the
// student. Deleted sessions are treated as absent to students.
func (a *App) sessionOwned(ctx context.Context, sessionID, studentID string) bool {
	s, err := a.Session(ctx, sessionID)
	return err == nil && s != nil && !s.Deleted && s.StudentID == studentID
}

// --- snapshots / usage / cost ---

func (a *App) SnapshotFor(ctx context.Context, sessionID string) (*Tokens, bool, error) {
	row := a.db.QueryRowContext(ctx,
		`SELECT input_tokens, output_tokens, cache_read_tokens FROM session_snapshots WHERE session_id=?`,
		sessionID)
	t := &Tokens{}
	err := row.Scan(&t.Input, &t.Output, &t.CacheRead)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return t, true, nil
}

func (a *App) UpsertSnapshot(ctx context.Context, sessionID string, t Tokens, at int64) error {
	_, err := a.db.ExecContext(ctx, `
		INSERT INTO session_snapshots(session_id, input_tokens, output_tokens, cache_read_tokens, updated_at)
		VALUES(?,?,?,?,?)
		ON CONFLICT(session_id) DO UPDATE SET
		  input_tokens=excluded.input_tokens,
		  output_tokens=excluded.output_tokens,
		  cache_read_tokens=excluded.cache_read_tokens,
		  updated_at=excluded.updated_at`,
		sessionID, t.Input, t.Output, t.CacheRead, at)
	return err
}

func (a *App) AddUsageEvent(ctx context.Context, e UsageEvent) error {
	_, err := a.db.ExecContext(ctx, `
		INSERT INTO usage_events(student_id, session_id, model, recorded_at,
		  input_tokens, output_tokens, cache_read_tokens, cost_micros)
		VALUES(?,?,?,?,?,?,?,?)`,
		e.StudentID, e.SessionID, e.Model, e.RecordedAt,
		e.InputTokens, e.OutputTokens, e.CacheReadTokens, e.CostMicros)
	return err
}

// SpendByStudent sums frozen cost for a student in micro-dollars.
func (a *App) SpendByStudent(ctx context.Context, studentID string) (int64, error) {
	var total sql.NullInt64
	err := a.db.QueryRowContext(ctx,
		`SELECT SUM(cost_micros) FROM usage_events WHERE student_id=?`, studentID).Scan(&total)
	if err != nil {
		return 0, err
	}
	return total.Int64, nil
}

// SpendBySession returns a student's frozen cost per session id.
func (a *App) SpendBySession(ctx context.Context, studentID string) (map[string]int64, error) {
	rows, err := a.db.QueryContext(ctx,
		`SELECT session_id, SUM(cost_micros) FROM usage_events WHERE student_id=? GROUP BY session_id`,
		studentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var sid string
		var sum int64
		if err := rows.Scan(&sid, &sum); err != nil {
			return nil, err
		}
		out[sid] = sum
	}
	return out, rows.Err()
}

func (a *App) RateFor(ctx context.Context, provider, model string) (*Rate, error) {
	row := a.db.QueryRowContext(ctx,
		`SELECT provider, model, multiplier, input_micros, output_micros, cache_read_micros, fetched_at
		 FROM model_rates WHERE provider=? AND model=?`, provider, model)
	r := &Rate{}
	err := row.Scan(&r.Provider, &r.Model, &r.Multiplier, &r.InputMicros, &r.OutputMicros, &r.CacheMicros, &r.FetchedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return r, nil
}

func (a *App) UpsertRate(ctx context.Context, r Rate) error {
	_, err := a.db.ExecContext(ctx, `
		INSERT INTO model_rates(provider, model, multiplier, input_micros, output_micros, cache_read_micros, fetched_at)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(provider, model) DO UPDATE SET
		  multiplier=excluded.multiplier,
		  input_micros=excluded.input_micros,
		  output_micros=excluded.output_micros,
		  cache_read_micros=excluded.cache_read_micros,
		  fetched_at=excluded.fetched_at`,
		r.Provider, r.Model, r.Multiplier, r.InputMicros, r.OutputMicros, r.CacheMicros, r.FetchedAt)
	return err
}
