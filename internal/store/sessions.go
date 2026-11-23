package store

import (
	"cmp"
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
	// Archived keeps the session out of the apps' feed and lists, until
	// it is used again.
	Archived bool
	Created  time.Time
	Updated  time.Time
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

// Touch marks session id as used now, which puts it first in Sessions and
// takes it out of the archive, and reports whether it was there.
func (s *Store) Touch(ctx context.Context, id string) (bool, error) {
	restored, err := s.Restore(ctx, id)
	if err != nil {
		return false, err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE sessions SET updated = ? WHERE id = ?`, unix(time.Now()), id)
	return restored, err
}

// Restore takes session id out of the archive, and reports whether it was
// there.
func (s *Store) Restore(ctx context.Context, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE sessions SET archived = 0 WHERE id = ? AND archived = 1`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// SetArchived puts session id in the archive or takes it out, or returns
// ErrNotFound. Taken out, it counts as used now, so ArchiveIdle does not put
// it back at once.
func (s *Store) SetArchived(ctx context.Context, id string, archived bool) error {
	query, args := `UPDATE sessions SET archived = 1 WHERE id = ?`, []any{id}
	if !archived {
		query, args = `UPDATE sessions SET archived = 0, updated = ? WHERE id = ?`, []any{unix(time.Now()), id}
	}
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return cmp.Or(err, ErrNotFound)
	}
	return nil
}

// ArchiveIdle puts the sessions last used before `before` in the archive,
// except those in keep and those with messages in the inbox, and returns
// their ids.
func (s *Store) ArchiveIdle(ctx context.Context, before time.Time, keep []string) ([]string, error) {
	query := `UPDATE sessions SET archived = 1 WHERE archived = 0 AND updated < ?
		AND id NOT IN (SELECT session_id FROM inbox)`
	args := []any{unix(before)}
	if len(keep) > 0 {
		query += ` AND id NOT IN (?` + repeat(", ?", len(keep)-1) + `)`
		for _, id := range keep {
			args = append(args, id)
		}
	}

	rows, err := s.db.QueryContext(ctx, query+` RETURNING id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
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
	// Job keeps the runs of the job of this name.
	Job string
	// Shelf keeps the sessions out of the archive, the archived ones, or
	// both.
	Shelf Shelf
	// Before keeps the sessions that come after it, the latest used first:
	// the next page after the one it ended.
	Before *Mark
	Limit  int
}

// Shelf is where Sessions looks: in the feed, the archive, or both.
type Shelf int

const (
	Feed Shelf = iota
	Archive
	Everywhere
)

// Mark is where a page of Sessions ended: its last session's use and id.
type Mark struct {
	Updated time.Time
	ID      string
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
	if f.Job != "" {
		query += ` AND job_id = ?`
		args = append(args, f.Job)
	}
	switch f.Shelf {
	case Feed:
		query += ` AND archived = 0`
	case Archive:
		query += ` AND archived = 1`
	}
	if f.Before != nil {
		at := unix(f.Before.Updated)
		query += ` AND (updated < ? OR updated = ? AND id < ?)`
		args = append(args, at, at, f.Before.ID)
	}
	// The id orders sessions used at once, so a page ends at one place.
	query += ` ORDER BY updated DESC, id DESC`
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

const sessionColumns = `id, kind, COALESCE(parent_id, ''), COALESCE(job_id, ''), COALESCE(title, ''), provider, model, effort, context, compacted, archived, created, updated`

type scanner interface{ Scan(dest ...any) error }

func scanSession(row scanner) (Session, error) {
	var session Session
	var created, updated int64
	err := row.Scan(&session.ID, &session.Kind, &session.Parent, &session.Job, &session.Title,
		&session.Choice.Provider, &session.Choice.Model, &session.Choice.Effort,
		&session.Context, &session.Compacted, &session.Archived, &created, &updated)
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
