package sessions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const index = `{"op":"put","id":"abc","role":"root","title":"","created":1794165718796}
{"op":"put","id":"abc","role":"root","title":"Plan the trip","created":1794165718796,"updated":1794165718796}
{"op":"put","id":"kid","role":"subagent","title":"child","created":1794165718796}
{"op":"put","id":"gone","role":"root","title":"deleted","created":1794165718796}
{"op":"delete","id":"gone"}
`

const log = `{"kind":"session_created","workspace":"/home/agent/space"}
{"kind":"item","type":"user","ts":1794165718796,"data":{"text":"Plan a trip to Lisbon"}}
{"kind":"item","type":"assistant","ts":1794165718900,"data":{"text":"Checking flights."}}
{"kind":"item","type":"tool_call","ts":1794165719000,"data":{"tool_name":"shell","arguments_json":"{\"command\":\"ls\\n -la\"}"}}
{"kind":"item","type":"tool_result","ts":1794165719100,"data":{"text":"HUGE OUTPUT"}}
{"kind":"item","type":"assistant","ts":1794165719200,"data":{"text":"Here is the plan."}}
`

func TestExport(t *testing.T) {
	root := t.TempDir()
	sessionsDir := filepath.Join(root, "sessions")
	archive := filepath.Join(root, "archive")

	write(t, filepath.Join(sessionsDir, "index.jsonl"), index)
	for _, id := range []string{"abc", "kid", "gone"} {
		write(t, filepath.Join(sessionsDir, id, "log.jsonl"), log)
	}

	stats, err := Export(sessionsDir, archive, false)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Written != 1 {
		t.Fatalf("want one root session written, got %+v", stats)
	}

	files, _ := filepath.Glob(filepath.Join(archive, "*", "*", "*.md"))
	if len(files) != 1 || !strings.Contains(files[0], "plan-the-trip-abc.md") {
		t.Fatalf("files: %v", files)
	}

	data, _ := os.ReadFile(files[0])
	doc := string(data)
	for _, want := range []string{"# Plan the trip", "user\n\nPlan a trip to Lisbon", "Checking flights.", "- `shell: ls -la`", "Here is the plan."} {
		if !strings.Contains(doc, want) {
			t.Errorf("missing %q in:\n%s", want, doc)
		}
	}
	if strings.Contains(doc, "HUGE OUTPUT") {
		t.Error("tool output must not be exported")
	}

	if stats, _ := Export(sessionsDir, archive, false); stats.Written != 0 || stats.Unchanged != 1 {
		t.Errorf("second export should change nothing: %+v", stats)
	}

	// A session exported again keeps its file.
	if stats, _ := Export(sessionsDir, archive, true); stats.Written != 1 {
		t.Errorf("forced export: %+v", stats)
	}
	if _, err := os.Stat(files[0]); err != nil {
		t.Fatalf("a re-exported session lost its file: %v", err)
	}

	before := time.UnixMilli(1794165718796 - 1)
	if got, _ := Recent(sessionsDir, archive, before); len(got) != 1 || got[0] != files[0] {
		t.Errorf("recent before the update: %v", got)
	}
	if got, _ := Recent(sessionsDir, archive, before.Add(time.Hour)); len(got) != 0 {
		t.Errorf("recent after the update: %v", got)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
