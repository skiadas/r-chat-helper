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

// Session is a student chat session, managed by the control plane (not by a
// separate agent runtime).
type Session struct {
	ID        string
	StudentID string
	Title     string
	CreatedAt int64
}

// Message is a single persisted chat turn.
type Message struct {
	ID        int64
	SessionID string
	Role      string
	Text      string
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

// --- sessions ---

func (a *App) CreateSession(ctx context.Context, studentID, title string) (*Session, error) {
	id := newID()
	_, err := a.db.ExecContext(ctx,
		`INSERT INTO sessions(id, student_id, title, created_at) VALUES(?,?,?,?)`,
		id, studentID, title, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	return &Session{ID: id, StudentID: studentID, Title: title}, nil
}

func (a *App) Session(ctx context.Context, id string) (*Session, error) {
	s := &Session{}
	err := a.db.QueryRowContext(ctx,
		`SELECT id, student_id, title, created_at FROM sessions WHERE id=?`, id).Scan(&s.ID, &s.StudentID, &s.Title, &s.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s, nil
}

func (a *App) ListSessions(ctx context.Context, studentID string) ([]Session, error) {
	rows, err := a.db.QueryContext(ctx,
		`SELECT id, student_id, title, created_at FROM sessions WHERE student_id=? ORDER BY created_at DESC`, studentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		s := Session{}
		if err := rows.Scan(&s.ID, &s.StudentID, &s.Title, &s.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// AddMessage appends a turn (role: user|assistant) and returns its persisted
// record. Returns an error if the session does not belong to the student.
func (a *App) AddMessage(ctx context.Context, sessionID, role, text string) error {
	_, err := a.db.ExecContext(ctx,
		`INSERT INTO messages(session_id, role, text, seq) SELECT ?,?,?, COALESCE(MAX(seq),0)+1 FROM messages WHERE session_id=?`,
		sessionID, role, text, sessionID)
	return err
}

func (a *App) Messages(ctx context.Context, sessionID string) ([]Message, error) {
	rows, err := a.db.QueryContext(ctx,
		`SELECT id, session_id, role, text, seq FROM messages WHERE session_id=? ORDER BY seq`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		m := Message{}
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &m.Text, &m.Seq); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// sessionOwned verifies a session exists and belongs to the student.
func (a *App) sessionOwned(ctx context.Context, sessionID, studentID string) bool {
	s, err := a.Session(ctx, sessionID)
	return err == nil && s != nil && s.StudentID == studentID
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
