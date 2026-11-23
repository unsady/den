package store

import (
	"context"
	"time"
)

// Message is one message that waits in the inbox for a turn of its
// session.
type Message struct {
	ID      int64
	Session string
	Text    string
	// Image is the saved path of a photo to attach.
	Image string
	// Report marks den's report of what came of an action, and Notice is
	// what den tells the user instead when the agent does not get it.
	Report  bool
	Notice  string
	Created time.Time
}

// Enqueue keeps messages for the next turns of their sessions, which must
// be in the store.
func (s *Store) Enqueue(ctx context.Context, messages ...Message) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now()
	for _, m := range messages {
		if m.Created.IsZero() {
			m.Created = now
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO inbox (session_id, text, image, report, notice, created) VALUES (?, ?, ?, ?, ?, ?)`,
			m.Session, m.Text, m.Image, m.Report, m.Notice, unix(m.Created)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Waiting says how many messages wait in the inbox.
func (s *Store) Waiting(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM inbox`).Scan(&n)
	return n, err
}

// Inbox lists the waiting messages, oldest first, and leaves them waiting.
func (s *Store) Inbox(ctx context.Context) ([]Message, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+messageColumns+` FROM inbox ORDER BY id`)
	if err != nil {
		return nil, err
	}
	return scanMessages(rows)
}

// NextTurn returns the session whose message waits longest, leaving out
// the busy ones, or "" when none wait.
func (s *Store) NextTurn(ctx context.Context, busy ...string) (string, error) {
	query := `SELECT session_id FROM inbox`
	args := make([]any, len(busy))
	if len(busy) > 0 {
		query += ` WHERE session_id NOT IN (?` + repeat(", ?", len(busy)-1) + `)`
		for i, id := range busy {
			args[i] = id
		}
	}
	var session string
	err := s.db.QueryRowContext(ctx, query+` ORDER BY id LIMIT 1`, args...).Scan(&session)
	if isNoRows(err) {
		return "", nil
	}
	return session, err
}

// TakeTurn takes session's messages out of the inbox, oldest first, for one
// turn.
func (s *Store) TakeTurn(ctx context.Context, session string) ([]Message, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `SELECT `+messageColumns+` FROM inbox WHERE session_id = ? ORDER BY id`, session)
	if err != nil {
		return nil, err
	}
	list, err := scanMessages(rows)
	if err != nil {
		return nil, err
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM inbox WHERE session_id = ?`, session); err != nil {
		return nil, err
	}
	return list, tx.Commit()
}

// ClearInbox drops the waiting messages of session, or every one when
// session is empty, and returns them.
func (s *Store) ClearInbox(ctx context.Context, session string) ([]Message, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	where, args := "", []any{}
	if session != "" {
		where, args = ` WHERE session_id = ?`, []any{session}
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+messageColumns+` FROM inbox`+where+` ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	list, err := scanMessages(rows)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM inbox`+where, args...); err != nil {
		return nil, err
	}
	return list, tx.Commit()
}

// Hold keeps the messages of session's turn that failed before its agent
// took a step, in place of any it held, so the user can retry them.
func (s *Store) Hold(ctx context.Context, session string, messages []Message) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM held WHERE session_id = ?`, session); err != nil {
		return err
	}
	for _, m := range messages {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO held (session_id, text, image, report, notice, created) VALUES (?, ?, ?, ?, ?, ?)`,
			session, m.Text, m.Image, m.Report, m.Notice, unix(m.Created)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Release puts session's held messages back in the inbox for its next
// turn, and says whether it held any.
func (s *Store) Release(ctx context.Context, session string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`INSERT INTO inbox (session_id, text, image, report, notice, created)
		SELECT session_id, text, image, report, notice, created FROM held WHERE session_id = ? ORDER BY id`, session)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM held WHERE session_id = ?`, session); err != nil {
		return false, err
	}
	return n > 0, tx.Commit()
}

// DropHeld forgets session's held messages, as when the user writes on.
func (s *Store) DropHeld(ctx context.Context, session string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM held WHERE session_id = ?`, session)
	return err
}

// Holds says whether session holds messages to retry.
func (s *Store) Holds(ctx context.Context, session string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM held WHERE session_id = ?`, session).Scan(&n)
	return n > 0, err
}

const messageColumns = `id, session_id, text, image, report, notice, created`

func scanMessages(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}) ([]Message, error) {
	defer rows.Close()
	var list []Message
	for rows.Next() {
		var m Message
		var created int64
		if err := rows.Scan(&m.ID, &m.Session, &m.Text, &m.Image, &m.Report, &m.Notice, &created); err != nil {
			return nil, err
		}
		m.Created = fromUnix(created)
		list = append(list, m)
	}
	return list, rows.Err()
}
