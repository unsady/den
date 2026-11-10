package jobs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

// Runs keeps run records as one file per run under dir/<job id>/.
type Runs struct {
	dir  string
	keep int
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

func (r *Runs) prune(dir string) error {
	names, err := r.names(dir)
	if err != nil {
		return err
	}
	for len(names) > r.keep {
		_ = os.Remove(filepath.Join(dir, names[0]))
		names = names[1:]
	}
	return nil
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
