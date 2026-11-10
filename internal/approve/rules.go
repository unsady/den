package approve

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"time"
)

const (
	// rulesFile keeps the kinds of action the user allowed for good.
	rulesFile = "approvals.json"
	// logFile keeps the actions that changed something outside, one JSON
	// per line.
	logFile = "actions.jsonl"
	// keptActions is how many actions the log keeps.
	keptActions = 500
)

// Rule is a kind of action the user allowed for good.
type Rule struct {
	Key   string    `json:"key"`
	Label string    `json:"label"`
	Added time.Time `json:"added"`
}

// ID names the rule briefly, for a button.
func (r Rule) ID() string {
	sum := sha256.Sum256([]byte(r.Key))
	return hex.EncodeToString(sum[:])[:12]
}

// Action is one entry of the log.
type Action struct {
	At      time.Time `json:"at"`
	What    string    `json:"what"`
	Outcome string    `json:"outcome"`
}

// Rules returns the kinds of action the user allowed for good, oldest
// first.
func (a *Asker) Rules() []Rule {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.loadRules()
}

// Forget removes the rule with id and returns it, or false when there is
// none.
func (a *Asker) Forget(id string) (Rule, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	rules := a.loadRules()
	for i, r := range rules {
		if r.ID() == id {
			if err := a.saveRules(append(rules[:i:i], rules[i+1:]...)); err != nil {
				log.Printf("approve: %v", err)
				return Rule{}, false
			}
			return r, true
		}
	}
	return Rule{}, false
}

func (a *Asker) allowed(key string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, r := range a.loadRules() {
		if r.Key == key {
			return true
		}
	}
	return false
}

func (a *Asker) allow(key, label string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	rules := a.loadRules()
	for _, r := range rules {
		if r.Key == key {
			return nil
		}
	}
	return a.saveRules(append(rules, Rule{Key: key, Label: label, Added: time.Now()}))
}

// loadRules reads the rules. The caller holds a.mu.
func (a *Asker) loadRules() []Rule {
	var rules []Rule
	data, err := os.ReadFile(filepath.Join(a.cfg.State, rulesFile))
	if err == nil {
		json.Unmarshal(data, &rules)
	}
	return rules
}

// saveRules writes the rules. The caller holds a.mu.
func (a *Asker) saveRules(rules []Rule) error {
	data, err := json.MarshalIndent(rules, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(a.cfg.State, rulesFile)
	if err := os.WriteFile(path+".tmp", append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// Note adds an action to the log, such as a request that wrote somewhere
// without a question.
func (a *Asker) Note(what, outcome string) {
	if a.cfg.State == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	line, _ := json.Marshal(Action{At: time.Now(), What: what, Outcome: outcome})
	path := filepath.Join(a.cfg.State, logFile)
	actions := a.readLog()
	if len(actions) >= 2*keptActions {
		// Rewrite the log with its newest actions once it holds twice as many.
		var buf bytes.Buffer
		for _, act := range actions[len(actions)-keptActions:] {
			kept, _ := json.Marshal(act)
			buf.Write(append(kept, '\n'))
		}
		buf.Write(append(line, '\n'))
		if err := os.WriteFile(path+".tmp", buf.Bytes(), 0o600); err == nil {
			os.Rename(path+".tmp", path)
		}
		return
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("approve: %v", err)
		return
	}
	defer f.Close()
	f.Write(append(line, '\n'))
}

// Actions returns the logged actions since a time, oldest first.
func (a *Asker) Actions(since time.Time) []Action {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []Action
	for _, act := range a.readLog() {
		if !act.At.Before(since) {
			out = append(out, act)
		}
	}
	return out
}

// readLog reads the whole log. The caller holds a.mu.
func (a *Asker) readLog() []Action {
	f, err := os.Open(filepath.Join(a.cfg.State, logFile))
	if err != nil {
		return nil
	}
	defer f.Close()

	var actions []Action
	scan := bufio.NewScanner(f)
	scan.Buffer(nil, 1<<20)
	for scan.Scan() {
		var act Action
		if json.Unmarshal(scan.Bytes(), &act) == nil {
			actions = append(actions, act)
		}
	}
	return actions
}
