package jobs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Run is the record of one job run.
type Run struct {
	JobID     string        `json:"job_id"`
	Started   time.Time     `json:"started"`
	Duration  time.Duration `json:"duration"`
	ExitCode  int           `json:"exit_code"`
	SessionID string        `json:"session_id,omitempty"`
	Output    string        `json:"output,omitempty"`
	Error     string        `json:"error,omitempty"`
	Stderr    string        `json:"stderr,omitempty"`
	Delivered bool          `json:"delivered"`
	// Model is the model the run used, empty when den could not tell.
	Model string `json:"model,omitempty"`
	// FellBack says why the run moved to the second model, such as the
	// main model's provider refusing it; empty when it did not.
	FellBack string `json:"fell_back,omitempty"`
	// InputTokens and OutputTokens are what fx reported for the run.
	InputTokens  int64 `json:"input_tokens,omitempty"`
	OutputTokens int64 `json:"output_tokens,omitempty"`
}

func (r Run) Status() string {
	if r.Error != "" {
		return "failed"
	}
	return "ok"
}

// Runs keeps run records as one file per run under dir/<job id>/, and
// the fx session of every run it saved in dir/sessions.
type Runs struct {
	dir  string
	keep int
	// Forget, when set, is told the fx session of each run that pruning
	// drops, so the session can go too.
	Forget func(session string)
}

func NewRuns(dir string) *Runs {
	return &Runs{dir: dir, keep: 50}
}

func (r *Runs) Save(run Run) error {
	dir := filepath.Join(r.dir, run.JobID)
	data, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return err
	}

	name := fmt.Sprintf("%d.json", run.Started.UnixNano())
	if err := writeFile(filepath.Join(dir, name), append(data, '\n')); err != nil {
		return err
	}
	// The list outlives the run's record, so its session stays a job's
	// after pruning, even when it could not be deleted.
	if run.SessionID != "" {
		if err := appendLine(filepath.Join(r.dir, "sessions"), run.SessionID); err != nil {
			return err
		}
	}
	return r.prune(dir)
}

// Last returns up to n runs of a job, newest first.
func (r *Runs) Last(jobID string, n int) ([]Run, error) {
	names, err := r.names(filepath.Join(r.dir, jobID))
	if err != nil {
		return nil, err
	}

	var runs []Run
	for i := len(names) - 1; i >= 0 && len(runs) < n; i-- {
		data, err := os.ReadFile(filepath.Join(r.dir, jobID, names[i]))
		if err != nil {
			continue
		}
		var run Run
		if json.Unmarshal(data, &run) == nil {
			runs = append(runs, run)
		}
	}
	return runs, nil
}

// Sessions returns the fx sessions of every job run den saved.
func (r *Runs) Sessions() map[string]bool {
	sessions := map[string]bool{}
	if data, err := os.ReadFile(filepath.Join(r.dir, "sessions")); err == nil {
		for id := range strings.FieldsSeq(string(data)) {
			sessions[id] = true
		}
	}
	jobs, _ := os.ReadDir(r.dir)
	for _, job := range jobs {
		if !job.IsDir() {
			continue
		}
		names, _ := r.names(filepath.Join(r.dir, job.Name()))
		for _, name := range names {
			data, err := os.ReadFile(filepath.Join(r.dir, job.Name(), name))
			if err != nil {
				continue
			}
			var run Run
			if json.Unmarshal(data, &run) == nil && run.SessionID != "" {
				sessions[run.SessionID] = true
			}
		}
	}
	return sessions
}

func (r *Runs) prune(dir string) error {
	names, err := r.names(dir)
	if err != nil {
		return err
	}
	for len(names) > r.keep {
		path := filepath.Join(dir, names[0])
		var run Run
		if data, err := os.ReadFile(path); err == nil && json.Unmarshal(data, &run) == nil && run.SessionID != "" && r.Forget != nil {
			r.Forget(run.SessionID)
		}
		_ = os.Remove(path)
		names = names[1:]
	}
	return nil
}

func appendLine(path, line string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.WriteString(line + "\n")
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

// names lists run files oldest first.
func (r *Runs) names(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var names []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" {
			names = append(names, e.Name())
		}
	}
	// Names are nanosecond timestamps of equal width for any date in use.
	sort.Strings(names)
	return names, nil
}
