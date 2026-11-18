package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
)

// Telegram is the Telegram chat's client name for Cursor and SetCursor.
const Telegram = "telegram"

// Cursor returns the session client is in, or ErrNotFound when it is in
// none yet.
func (s *Store) Cursor(ctx context.Context, client string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT session_id FROM cursors WHERE client = ?`, client).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return id, err
}

// SetCursor puts client in session, which must be in the store.
func (s *Store) SetCursor(ctx context.Context, client, session string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO cursors (client, session_id) VALUES (?, ?)
		ON CONFLICT (client) DO UPDATE SET session_id = excluded.session_id`, client, session)
	return err
}

// ClearCursor takes client out of its session, so its next message starts a
// new one.
func (s *Store) ClearCursor(ctx context.Context, client string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM cursors WHERE client = ?`, client)
	return err
}

// offsetKey keeps the next Telegram update to read.
const offsetKey = "telegram.offset"

// TelegramOffset returns the next Telegram update to read, 0 at first.
func (s *Store) TelegramOffset(ctx context.Context) (int64, error) {
	value, err := s.Setting(ctx, offsetKey)
	if err != nil || value == "" {
		return 0, err
	}
	return strconv.ParseInt(value, 10, 64)
}

// SetTelegramOffset records that the updates before next were read.
func (s *Store) SetTelegramOffset(ctx context.Context, next int64) error {
	return s.SetSetting(ctx, offsetKey, strconv.FormatInt(next, 10))
}

// keptMessages is how many of the bot's latest messages a reply can go back
// to.
const keptMessages = 1000

// SentTelegram ties the bot's messages to the session that wrote them, so a
// reply to one goes on there.
func (s *Store) SentTelegram(ctx context.Context, session string, messages ...int64) error {
	if len(messages) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, id := range messages {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO telegram_messages (message_id, session_id) VALUES (?, ?)
			ON CONFLICT (message_id) DO UPDATE SET session_id = excluded.session_id`, id, session); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM telegram_messages WHERE message_id NOT IN
		(SELECT message_id FROM telegram_messages ORDER BY message_id DESC LIMIT ?)`, keptMessages); err != nil {
		return err
	}
	return tx.Commit()
}

// TelegramSession returns the session that wrote the bot's message id, or
// ErrNotFound.
func (s *Store) TelegramSession(ctx context.Context, message int64) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT session_id FROM telegram_messages WHERE message_id = ?`, message).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return id, err
}
