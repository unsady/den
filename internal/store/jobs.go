package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Job is a scheduled job. Its Name is a slug that identifies it, and its
// Title is what the user reads, in their language.
type Job struct {
	Name     string `json:"name"`
	Title    string `json:"title"`
	Schedule string `json:"schedule"`
	Prompt   string `json:"prompt"`
	// Slot is which of the chat's two models the job runs on: "second", or
	// empty for the main one.
	Slot    string    `json:"slot,omitempty"`
	Silent  bool      `json:"silent,omitempty"`
	Once    bool      `json:"once,omitempty"`
	NextRun time.Time `json:"next_run"`
	Created time.Time `json:"created"`

	LastRun    *time.Time `json:"last_run,omitempty"`
	LastStatus string     `json:"last_status,omitempty"`
}

// ErrJobExists says a job by that name is in the store already.
var ErrJobExists = errors.New("a job by that name exists")

// AddJob records a new job.
func (s *Store) AddJob(ctx context.Context, j Job) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO jobs (name, title, schedule, prompt, slot, silent, once, next_run, created, last_run, last_status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		j.Name, j.Title, j.Schedule, j.Prompt, j.Slot, j.Silent, j.Once, unix(j.NextRun), unix(j.Created),
		nullTime(j.LastRun), j.LastStatus)
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return ErrJobExists
	}
	return err
}

// Job returns the job by name, or ErrNotFound.
func (s *Store) Job(ctx context.Context, name string) (Job, error) {
	j, err := scanJob(s.db.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM jobs WHERE name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	return j, err
}

// Jobs lists every job, soonest first.
func (s *Store) Jobs(ctx context.Context) ([]Job, error) {
	return s.queryJobs(ctx, s.db, `SELECT `+jobColumns+` FROM jobs ORDER BY next_run, name`)
}

// RemoveJob deletes the job by name and returns it, or ErrNotFound. Its
// runs and sessions stay.
func (s *Store) RemoveJob(ctx context.Context, name string) (Job, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()

	j, err := scanJob(tx.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM jobs WHERE name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, err
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM jobs WHERE name = ?`, name); err != nil {
		return Job{}, err
	}
	return j, tx.Commit()
}

// SetJobSlot makes the job run on slot, "second" or "" for the main model.
func (s *Store) SetJobSlot(ctx context.Context, name, slot string) (Job, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE jobs SET slot = ? WHERE name = ?`, slot, name)
	if err != nil {
		return Job{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Job{}, ErrNotFound
	}
	return s.Job(ctx, name)
}

// FinishJob records the outcome of a run on the job, if it still exists.
func (s *Store) FinishJob(ctx context.Context, name string, at time.Time, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE jobs SET last_run = ?, last_status = ? WHERE name = ?`, unix(at), status, name)
	return err
}

// TakeDueJobs returns the jobs due at now that skip does not exclude. It
// moves each recurring one to the run next gives and removes each one-shot
// before it runs, so a crash mid-run never repeats a job. A next that
// fails tries the job again in an hour and records why.
func (s *Store) TakeDueJobs(ctx context.Context, now time.Time, skip func(name string) bool, next func(Job) (time.Time, error)) ([]Job, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	list, err := s.queryJobs(ctx, tx, `SELECT `+jobColumns+` FROM jobs WHERE next_run <= ? ORDER BY next_run, name`, unix(now))
	if err != nil {
		return nil, err
	}

	var due []Job
	for _, j := range list {
		if skip(j.Name) {
			continue
		}
		due = append(due, j)

		if j.Once {
			if _, err := tx.ExecContext(ctx, `DELETE FROM jobs WHERE name = ?`, j.Name); err != nil {
				return nil, err
			}
			continue
		}
		at, err := next(j)
		status := j.LastStatus
		if err != nil {
			at = now.Add(time.Hour)
			status = err.Error()
		}
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET next_run = ?, last_status = ? WHERE name = ?`, unix(at), status, j.Name); err != nil {
			return nil, err
		}
	}
	return due, tx.Commit()
}

const jobColumns = `name, title, schedule, prompt, slot, silent, once, next_run, created, last_run, last_status`

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func (s *Store) queryJobs(ctx context.Context, q querier, query string, args ...any) ([]Job, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, j)
	}
	return list, rows.Err()
}

func scanJob(row scanner) (Job, error) {
	var j Job
	var next, created int64
	var last sql.NullInt64
	err := row.Scan(&j.Name, &j.Title, &j.Schedule, &j.Prompt, &j.Slot, &j.Silent, &j.Once, &next, &created, &last, &j.LastStatus)
	if err != nil {
		return Job{}, err
	}
	j.NextRun, j.Created = fromUnix(next), fromUnix(created)
	if last.Valid {
		at := fromUnix(last.Int64)
		j.LastRun = &at
	}
	return j, nil
}

func nullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return unix(*t)
}

// Run is the record of one job run.
type Run struct {
	Job       string        `json:"job"`
	Started   time.Time     `json:"started"`
	Duration  time.Duration `json:"duration"`
	SessionID string        `json:"session_id,omitempty"`
	Output    string        `json:"output,omitempty"`
	Error     string        `json:"error,omitempty"`
	Stderr    string        `json:"stderr,omitempty"`
	Delivered bool          `json:"delivered"`
	// Model is the model the run used, empty when den could not tell.
	Model string `json:"model,omitempty"`
	// InputTokens and OutputTokens are what fx reported for the run.
	InputTokens  int64 `json:"input_tokens,omitempty"`
	OutputTokens int64 `json:"output_tokens,omitempty"`
}

// Status is how the run ended: ok or failed.
func (r Run) Status() string {
	if r.Error != "" {
		return "failed"
	}
	return "ok"
}

// keptRuns is how many of a job's latest runs the store keeps.
const keptRuns = 50

// AddRun records a run and drops the job's runs past the latest keptRuns.
func (s *Store) AddRun(ctx context.Context, r Run) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO job_runs (job, session_id, started, duration, output, error, stderr, delivered, model, input_tokens, output_tokens)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Job, r.SessionID, unix(r.Started), r.Duration.Milliseconds(), r.Output, r.Error, r.Stderr,
		r.Delivered, r.Model, r.InputTokens, r.OutputTokens); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM job_runs WHERE job = ? AND id NOT IN
		(SELECT id FROM job_runs WHERE job = ? ORDER BY started DESC, id DESC LIMIT ?)`, r.Job, r.Job, keptRuns); err != nil {
		return err
	}
	return tx.Commit()
}

// Runs returns up to n of the job's latest runs, newest first.
func (s *Store) Runs(ctx context.Context, job string, n int) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT job, session_id, started, duration, output, error, stderr, delivered, model, input_tokens, output_tokens
		FROM job_runs WHERE job = ? ORDER BY started DESC, id DESC LIMIT ?`, job, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Run
	for rows.Next() {
		var r Run
		var started, duration int64
		if err := rows.Scan(&r.Job, &r.SessionID, &started, &duration, &r.Output, &r.Error, &r.Stderr,
			&r.Delivered, &r.Model, &r.InputTokens, &r.OutputTokens); err != nil {
			return nil, err
		}
		r.Started, r.Duration = fromUnix(started), time.Duration(duration)*time.Millisecond
		list = append(list, r)
	}
	return list, rows.Err()
}
