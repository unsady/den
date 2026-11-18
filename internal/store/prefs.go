package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Slot is one of the chat's two models.
type Slot string

const (
	// Main is the model the chat and most jobs run on.
	Main Slot = "main"
	// Second is the model the jobs set to it run on.
	Second Slot = "second"
)

// Model returns the model chosen for slot, and false when none is, which
// leaves fx's own choice for Main.
func (s *Store) Model(ctx context.Context, slot Slot) (Choice, bool, error) {
	var c Choice
	err := s.db.QueryRowContext(ctx, `SELECT provider, model, effort FROM models WHERE slot = ?`, slot).
		Scan(&c.Provider, &c.Model, &c.Effort)
	if isNoRows(err) {
		return Choice{}, false, nil
	}
	return c, err == nil, err
}

// SetModel chooses the model for slot.
func (s *Store) SetModel(ctx context.Context, slot Slot, c Choice) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO models (slot, provider, model, effort) VALUES (?, ?, ?, ?)
		ON CONFLICT (slot) DO UPDATE SET provider = excluded.provider, model = excluded.model, effort = excluded.effort`,
		slot, c.Provider, c.Model, c.Effort)
	return err
}

// ClearModel leaves slot without a model of its own.
func (s *Store) ClearModel(ctx context.Context, slot Slot) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM models WHERE slot = ?`, slot)
	return err
}

// keptRecent is how many models RecentModels remembers.
const keptRecent = 20

// UseModel puts provider's model first among the ones chosen last.
func (s *Store) UseModel(ctx context.Context, provider, model string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO recent_models (provider, model, used) VALUES (?, ?, ?)
		ON CONFLICT (provider, model) DO UPDATE SET used = excluded.used`,
		provider, model, time.Now().UnixNano()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM recent_models WHERE rowid NOT IN
		(SELECT rowid FROM recent_models ORDER BY used DESC LIMIT ?)`, keptRecent); err != nil {
		return err
	}
	return tx.Commit()
}

// RecentModels returns the models chosen last, newest first, without
// efforts.
func (s *Store) RecentModels(ctx context.Context) ([]Choice, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT provider, model FROM recent_models ORDER BY used DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Choice
	for rows.Next() {
		var c Choice
		if err := rows.Scan(&c.Provider, &c.Model); err != nil {
			return nil, err
		}
		list = append(list, c)
	}
	return list, rows.Err()
}

// Setting returns the value kept under key, "" when there is none.
func (s *Store) Setting(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if isNoRows(err) {
		return "", nil
	}
	return value, err
}

// SetSetting keeps value under key.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }
