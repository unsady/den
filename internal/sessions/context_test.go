package sessions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestContextSize(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "s1"), 0o700)
	log := strings.Join([]string{
		`{"kind":"item","type":"model_call","ts":1000,"data":{"purpose":"turn","outcome":"completed","input_tokens":5000}}`,
		`{"kind":"item","type":"model_call","ts":2000,"data":{"purpose":"turn","outcome":"completed","input_tokens":7696}}`,
		`{"kind":"item","type":"model_call","ts":2100,"data":{"purpose":"compaction","outcome":"completed","input_tokens":9000}}`,
		`{"kind":"item","type":"model_call","ts":2200,"data":{"purpose":"turn","outcome":"failed"}}`,
		`{"kind":"item","type":"assistant","ts":2300,"data":{"text":"hi"}}`,
	}, "\n")
	os.WriteFile(filepath.Join(dir, "s1", "log.jsonl"), []byte(log), 0o600)

	if got := ContextSize(dir, "s1", time.UnixMilli(1500)); got != 7696 {
		t.Fatalf("size %d, want the last turn call's 7696", got)
	}
	if got := ContextSize(dir, "s1", time.UnixMilli(3000)); got != 0 {
		t.Fatalf("size %d after the last call", got)
	}
	if got := ContextSize(dir, "none", time.Time{}); got != 0 {
		t.Fatalf("size %d of a missing session", got)
	}
}
