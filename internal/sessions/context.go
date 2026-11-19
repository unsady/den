package sessions

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// ContextSize returns how big session id's prompt was at its last model
// call since the time given, in tokens, or 0 when fx logged none. fx counts
// the whole prompt, cached or not, and logs each call in the session's own
// log, so the size is the session's whatever runs besides it.
func ContextSize(sessionsDir, id string, since time.Time) int {
	f, err := os.Open(filepath.Join(sessionsDir, id, "log.jsonl"))
	if err != nil {
		return 0
	}
	defer f.Close()

	size := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 64<<20)
	for scanner.Scan() {
		var e event
		if json.Unmarshal(scanner.Bytes(), &e) != nil || e.Type != "model_call" || e.TS < since.UnixMilli() {
			continue
		}
		var call struct {
			Purpose     string `json:"purpose"`
			Outcome     string `json:"outcome"`
			InputTokens int    `json:"input_tokens"`
		}
		if json.Unmarshal(e.Data, &call) != nil || call.Purpose != "turn" || call.Outcome != "completed" || call.InputTokens == 0 {
			continue
		}
		size = call.InputTokens
	}
	return size
}
