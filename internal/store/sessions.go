package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
)

// Kind is what started a session.
type Kind string

const (
	// Chat is a session the user talks in.
	KindChat Kind = "chat"
	// Background is a task /bg runs besides the chat.
	KindBackground Kind = "bg"
	// Job is a job's run that wrote to the user.
	KindJob Kind = "job"
)

// Choice is the model a session runs on. Empty fields take the chat's.
type Choice struct {
	Provider string
	Model    string
	Effort   string
}

// Session is what den knows of one fx session.
type Session struct {
	ID   string
	Kind Kind
	// Parent is the session this one was started from, empty for none.
	Parent string
	// Job is the job whose run this is, empty for other kinds.
	Job string
	// Title is empty until den names the session.
	Title  string
	Choice Choice
	// Context is the session's size after its last turn, in tokens, 0 when
	// unknown, and Compacted its size before den last compacted it, until
	// the user hears of it.
	Context   int
	Compacted int
	Created   time.Time
	Updated   time.Time
}

// NewID names a new session as fx names one: 9 random bytes in URL-safe
// base64, not starting with '-'.
func NewID() string {
	b := make([]byte, 9)
	for {
		rand.Read(b)
		if id := base64.RawURLEncoding.EncodeToString(b); id[0] != '-' {
			return id
		}
	}
}

// AddSession records a new session. Its Created and Updated default to now.
func (s *Store) AddSession(ctx context.Context, session Session) error {
	if session.ID == "" {
		return errors.New("store: a session needs an id")
	}
	now := time.Now()
	if session.Created.IsZero() {
		session.Created = now
	}
	if session.Updated.IsZero() {
		session.Updated = session.Created
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (id, kind, parent_id, job_id, title, provider, model, effort, created, updated)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		session.ID, session.Kind, null(session.Parent), null(session.Job), null(session.Title),
		session.Choice.Provider, session.Choice.Model, session.Choice.Effort,
		unix(session.Created), unix(session.Updated))
	if err != nil {
		return fmt.Errorf("store: add session %s: %w", session.ID, err)
	}
	return nil
}

// Session returns the session id, or ErrNotFound.
func (s *Store) Session(ctx context.Context, id string) (Session, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM sessions WHERE id = ?`, id)
	session, err := scanSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	return session, err
}

// SetTitle names session id. It leaves a title already set as it is, so a
// late answer from the model cannot replace one, and reports whether it set
// it.
func (s *Store) SetTitle(ctx context.Context, id, title string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE sessions SET title = ? WHERE id = ? AND title IS NULL`, title, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Touch marks session id as used now, which puts it first in Sessions.
func (s *Store) Touch(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET updated = ? WHERE id = ?`, unix(time.Now()), id)
	return err
}

// SetContext records session id's size after a turn, in tokens.
func (s *Store) SetContext(ctx context.Context, id string, tokens int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET context = ? WHERE id = ?`, tokens, id)
	return err
}

// SetCompacted records session id's size before den compacted it, or 0
// once the user has heard of it.
func (s *Store) SetCompacted(ctx context.Context, id string, before int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET compacted = ? WHERE id = ?`, before, id)
	return err
}

// Filter narrows Sessions.
type Filter struct {
	// Kinds keeps the sessions of these kinds, all when empty.
	Kinds []Kind
	// Query keeps the titles that contain it, ignoring case.
	Query string
	Limit int
}

// Sessions lists sessions, the latest used first.
func (s *Store) Sessions(ctx context.Context, f Filter) ([]Session, error) {
	query := `SELECT ` + sessionColumns + ` FROM sessions WHERE 1 = 1`
	var args []any
	if len(f.Kinds) > 0 {
		query += ` AND kind IN (?` + repeat(", ?", len(f.Kinds)-1) + `)`
		for _, k := range f.Kinds {
			args = append(args, k)
		}
	}
	if f.Query != "" {
		query += ` AND title LIKE ? ESCAPE '\'`
		args = append(args, "%"+escapeLike(f.Query)+"%")
	}
	query += ` ORDER BY updated DESC`
	if f.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, f.Limit)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Session
	for rows.Next() {
		session, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, session)
	}
	return list, rows.Err()
}

// DeleteSession forgets session id, with the cursors in it and the bot's
// messages it wrote. It is not an error when there is none.
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	return err
}

const sessionColumns = `id, kind, COALESCE(parent_id, ''), COALESCE(job_id, ''), COALESCE(title, ''), provider, model, effort, context, compacted, created, updated`

type scanner interface{ Scan(dest ...any) error }

func scanSession(row scanner) (Session, error) {
	var session Session
	var created, updated int64
	err := row.Scan(&session.ID, &session.Kind, &session.Parent, &session.Job, &session.Title,
		&session.Choice.Provider, &session.Choice.Model, &session.Choice.Effort,
		&session.Context, &session.Compacted, &created, &updated)
	session.Created, session.Updated = fromUnix(created), fromUnix(updated)
	return session, err
}

// null stores an empty string as NULL.
func null(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func repeat(s string, n int) string {
	out := ""
	for range n {
		out += s
	}
	return out
}

func escapeLike(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '%' || r == '_' || r == '\\' {
			out = append(out, '\\')
		}
		out = append(out, r)
	}
	return string(out)
}

// SetSessionJob records which job's run session is.
func (s *Store) SetSessionJob(ctx context.Context, id, job string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET job_id = ? WHERE id = ?`, job, id)
	return err
}
