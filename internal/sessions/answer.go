package sessions

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// reportPrefix starts the turn fx adds when a subagent reports after the
// turn that started it ended.
const reportPrefix = "Subagent report"

// Answer returns the last reply fx gave in session id since the time given
// to a turn that was not a subagent's report, or "" when there is none. A
// report that comes after the answer starts a turn of its own, and fx ask
// returns only the reply to that last turn, such as "Done.", which hides
// the answer itself.
func Answer(sessionsDir, id string, since time.Time) string {
	f, err := os.Open(filepath.Join(sessionsDir, id, "log.jsonl"))
	if err != nil {
		return ""
	}
	defer f.Close()

	answer := ""
	reporting := false
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 64<<20)
	for scanner.Scan() {
		var e event
		if json.Unmarshal(scanner.Bytes(), &e) != nil || e.Kind != "item" || e.TS < since.UnixMilli() {
			continue
		}

		switch e.Type {
		case "user":
			reporting = strings.HasPrefix(textOf(e.Data), reportPrefix)
		case "assistant":
			if text := textOf(e.Data); text != "" && !reporting {
				answer = text
			}
		}
	}
	return answer
}
