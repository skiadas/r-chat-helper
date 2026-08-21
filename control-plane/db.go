package controlplane

import (
	"database/sql"

	_ "modernc.org/sqlite"
)

// OpenDB opens (creating if needed) the control-plane SQLite database and
// applies the schema.
func OpenDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
		"PRAGMA synchronous=NORMAL",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			db.Close()
			return nil, err
		}
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS students (
  id            TEXT PRIMARY KEY,
  email         TEXT UNIQUE NOT NULL,
  name          TEXT NOT NULL,
  budget_micros INTEGER NOT NULL DEFAULT 0,
  active        INTEGER NOT NULL DEFAULT 1,
  created_at    INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
  id         TEXT PRIMARY KEY,
  student_id TEXT NOT NULL REFERENCES students(id),
  title      TEXT,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_student ON sessions(student_id);

CREATE TABLE IF NOT EXISTS messages (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL REFERENCES sessions(id),
  role       TEXT NOT NULL,
  text       TEXT NOT NULL,
  seq        INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id, seq);

CREATE TABLE IF NOT EXISTS session_snapshots (
  session_id        TEXT PRIMARY KEY,
  input_tokens      INTEGER NOT NULL,
  output_tokens     INTEGER NOT NULL,
  cache_read_tokens INTEGER NOT NULL,
  updated_at        INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS usage_events (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  student_id        TEXT NOT NULL REFERENCES students(id),
  session_id        TEXT NOT NULL,
  model             TEXT NOT NULL,
  recorded_at       INTEGER NOT NULL,
  input_tokens      INTEGER NOT NULL,
  output_tokens     INTEGER NOT NULL,
  cache_read_tokens INTEGER NOT NULL,
  cost_micros       INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_usage_student ON usage_events(student_id);
CREATE INDEX IF NOT EXISTS idx_usage_session ON usage_events(session_id);

CREATE TABLE IF NOT EXISTS model_rates (
  provider          TEXT NOT NULL,
  model             TEXT NOT NULL,
  multiplier        REAL NOT NULL DEFAULT 1,
  input_micros      INTEGER NOT NULL,
  output_micros     INTEGER NOT NULL,
  cache_read_micros INTEGER NOT NULL,
  fetched_at        INTEGER NOT NULL,
  PRIMARY KEY (provider, model)
);
`
	if _, err := db.Exec(schema); err != nil {
		return err
	}
	return nil
}
