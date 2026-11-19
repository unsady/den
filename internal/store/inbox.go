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
	// App marks a message from the app, whose turn answers only there.
	App bool
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
			`INSERT INTO inbox (session_id, text, image, app, report, notice, created) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			m.Session, m.Text, m.Image, m.App, m.Report, m.Notice, unix(m.Created)); err != nil {
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

// NextTurn returns the session whose message waits longest, or "" when
// none wait.
func (s *Store) NextTurn(ctx context.Context) (string, error) {
	var session string
	err := s.db.QueryRowContext(ctx, `SELECT session_id FROM inbox ORDER BY id LIMIT 1`).Scan(&session)
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

// ClearInbox drops every waiting message and returns them.
func (s *Store) ClearInbox(ctx context.Context) ([]Message, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `SELECT `+messageColumns+` FROM inbox ORDER BY id`)
	if err != nil {
		return nil, err
	}
	list, err := scanMessages(rows)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM inbox`); err != nil {
		return nil, err
	}
	return list, tx.Commit()
}

const messageColumns = `id, session_id, text, image, app, report, notice, created`

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
		if err := rows.Scan(&m.ID, &m.Session, &m.Text, &m.Image, &m.App, &m.Report, &m.Notice, &created); err != nil {
			return nil, err
		}
		m.Created = fromUnix(created)
		list = append(list, m)
	}
	return list, rows.Err()
}
