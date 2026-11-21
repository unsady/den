// Package store keeps den's own state in one SQLite database: the sessions
// and what they are, which session each client is in, which sessions
// Telegram's chat is in, and which session wrote each of the bot's
// messages.
//
// fx keeps the sessions' transcripts in its own files; the store holds what
// den knows about them, and is the only place it keeps it.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// File is the database's name in den's state directory.
const File = "den.db"

// Store is den's database. It is safe for concurrent use.
type Store struct {
	db *sql.DB
}

// Open opens the database at path, creating it and bringing its schema up
// to date.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}

	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection serializes writes, so den never waits on SQLite's lock.
	db.SetMaxOpenConns(1)

	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// migrations are the schema's versions in order. A database at version n
// has run the first n. Never change one that shipped; add another.
var migrations = []string{
	`CREATE TABLE sessions (
		id        TEXT PRIMARY KEY,
		kind      TEXT NOT NULL CHECK (kind IN ('chat', 'bg', 'job')),
		parent_id TEXT REFERENCES sessions(id) ON DELETE SET NULL ON UPDATE CASCADE,
		job_id    TEXT,
		title     TEXT,
		provider  TEXT NOT NULL DEFAULT '',
		model     TEXT NOT NULL DEFAULT '',
		effort    TEXT NOT NULL DEFAULT '',
		created   INTEGER NOT NULL,
		updated   INTEGER NOT NULL
	);
	CREATE INDEX sessions_updated ON sessions(updated DESC);

	CREATE TABLE cursors (
		client     TEXT PRIMARY KEY,
		session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE ON UPDATE CASCADE
	);

	CREATE TABLE telegram_messages (
		message_id INTEGER PRIMARY KEY,
		session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE ON UPDATE CASCADE
	);`,

	`ALTER TABLE sessions ADD COLUMN context INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE sessions ADD COLUMN compacted INTEGER NOT NULL DEFAULT 0;

	CREATE TABLE inbox (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE ON UPDATE CASCADE,
		text       TEXT NOT NULL DEFAULT '',
		image      TEXT NOT NULL DEFAULT '',
		app        INTEGER NOT NULL DEFAULT 0,
		report     INTEGER NOT NULL DEFAULT 0,
		notice     TEXT NOT NULL DEFAULT '',
		created    INTEGER NOT NULL
	);

	CREATE TABLE models (
		slot     TEXT PRIMARY KEY CHECK (slot IN ('main', 'second')),
		provider TEXT NOT NULL,
		model    TEXT NOT NULL,
		effort   TEXT NOT NULL DEFAULT ''
	);

	CREATE TABLE recent_models (
		provider TEXT NOT NULL,
		model    TEXT NOT NULL,
		used     INTEGER NOT NULL,
		PRIMARY KEY (provider, model)
	);

	CREATE TABLE settings (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);`,

	`CREATE TABLE jobs (
		name        TEXT PRIMARY KEY,
		title       TEXT NOT NULL,
		schedule    TEXT NOT NULL,
		prompt      TEXT NOT NULL,
		slot        TEXT NOT NULL DEFAULT '' CHECK (slot IN ('', 'second')),
		silent      INTEGER NOT NULL DEFAULT 0,
		once        INTEGER NOT NULL DEFAULT 0,
		next_run    INTEGER NOT NULL,
		created     INTEGER NOT NULL,
		last_run    INTEGER,
		last_status TEXT NOT NULL DEFAULT ''
	);

	CREATE TABLE job_runs (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		job           TEXT NOT NULL,
		session_id    TEXT NOT NULL DEFAULT '',
		started       INTEGER NOT NULL,
		duration      INTEGER NOT NULL DEFAULT 0,
		output        TEXT NOT NULL DEFAULT '',
		error         TEXT NOT NULL DEFAULT '',
		stderr        TEXT NOT NULL DEFAULT '',
		delivered     INTEGER NOT NULL DEFAULT 0,
		model         TEXT NOT NULL DEFAULT '',
		input_tokens  INTEGER NOT NULL DEFAULT 0,
		output_tokens INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX job_runs_job ON job_runs(job, started);

	ALTER TABLE sessions ADD COLUMN replied INTEGER NOT NULL DEFAULT 0;`,

	`CREATE TABLE telegram_sessions (
		session_id TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE ON UPDATE CASCADE
	);
	INSERT INTO telegram_sessions (session_id) SELECT session_id FROM cursors WHERE client = 'telegram';

	ALTER TABLE inbox DROP COLUMN app;`,
}

func (s *Store) migrate(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > len(migrations) {
		return fmt.Errorf("the database is at version %d, newer than this den's %d", version, len(migrations))
	}

	for i := version; i < len(migrations); i++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		// PRAGMA takes no parameters.
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// ErrNotFound says a session, a client's cursor, or a job is not in the
// store.
var ErrNotFound = errors.New("not found")

func unix(t time.Time) int64 { return t.UnixMilli() }

func fromUnix(ms int64) time.Time { return time.UnixMilli(ms) }
