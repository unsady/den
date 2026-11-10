// Package usage keeps the tokens fx reports, per day, for chat turns and
// jobs, so the chat's /usage can show what den spends.
package usage

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// keep is how many days of usage the store holds.
const keep = 60

// Tokens is what one kind of run used in a day.
type Tokens struct {
	Input  int64 `json:"input"`
	Output int64 `json:"output"`
	Runs   int   `json:"runs"`
}

func (t *Tokens) add(o Tokens) {
	t.Input += o.Input
	t.Output += o.Output
	t.Runs += o.Runs
}

// Store is usage.json in den's state: day → kind → tokens.
type Store struct {
	path string

	mu   sync.Mutex
	days map[string]map[string]Tokens
}

func Open(state string) *Store {
	s := &Store{path: filepath.Join(state, "usage.json"), days: map[string]map[string]Tokens{}}
	if data, err := os.ReadFile(s.path); err == nil {
		if err := json.Unmarshal(data, &s.days); err != nil {
			log.Printf("usage: %v", err)
		}
	}
	return s
}

// Add records one run of kind, such as "chat" or "jobs", at the given time.
func (s *Store) Add(kind string, at time.Time, input, output int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	day := at.Format(time.DateOnly)
	if s.days[day] == nil {
		s.days[day] = map[string]Tokens{}
	}
	t := s.days[day][kind]
	t.add(Tokens{Input: input, Output: output, Runs: 1})
	s.days[day][kind] = t

	cutoff := at.AddDate(0, 0, -keep).Format(time.DateOnly)
	for d := range s.days {
		if d < cutoff {
			delete(s.days, d)
		}
	}
	s.save()
}

// Report describes today, the last 7 days, and the last 30 days.
func (s *Store) Report(now time.Time) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	var b strings.Builder
	today := s.total(now, 1)
	b.WriteString("Today\n")
	if len(today) == 0 {
		b.WriteString("nothing yet\n")
	}
	for _, kind := range kinds(today) {
		t := today[kind]
		fmt.Fprintf(&b, "%s: %s in, %s out, %d %s\n", kind, count(t.Input), count(t.Output), t.Runs, runs(kind, t.Runs))
	}
	for _, days := range []int{7, 30} {
		var t Tokens
		for _, k := range s.total(now, days) {
			t.add(k)
		}
		fmt.Fprintf(&b, "\nLast %d days: %s in, %s out", days, count(t.Input), count(t.Output))
	}
	b.WriteString("\n\nTokens are as fx reports them; some providers do not report every request.")
	return b.String()
}

// total sums each kind over the given number of days up to now.
func (s *Store) total(now time.Time, days int) map[string]Tokens {
	sum := map[string]Tokens{}
	for i := range days {
		for kind, t := range s.days[now.AddDate(0, 0, -i).Format(time.DateOnly)] {
			k := sum[kind]
			k.add(t)
			sum[kind] = k
		}
	}
	return sum
}

func (s *Store) save() {
	data, _ := json.Marshal(s.days)
	tmp := s.path + ".tmp"
	err := os.WriteFile(tmp, data, 0o600)
	if err == nil {
		err = os.Rename(tmp, s.path)
	}
	if err != nil {
		log.Printf("usage: save: %v", err)
	}
}

func kinds(m map[string]Tokens) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func runs(kind string, n int) string {
	word := "run"
	if kind == "chat" {
		word = "turn"
	}
	if n != 1 {
		word += "s"
	}
	return word
}

// count writes 1234567 as 1.2M and 45600 as 45.6k.
func count(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}
