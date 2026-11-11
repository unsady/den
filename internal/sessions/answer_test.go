package sessions

import (
	"path/filepath"
	"testing"
	"time"
)

func TestAnswerSkipsLateReports(t *testing.T) {
	dir := t.TempDir()
	since := time.UnixMilli(1000)
	write(t, filepath.Join(dir, "s1", "log.jsonl"), `{"kind":"item","type":"user","ts":500,"data":{"text":"earlier"}}
{"kind":"item","type":"assistant","ts":600,"data":{"text":"an earlier answer"}}
{"kind":"item","type":"user","ts":1000,"data":{"text":"Check the three leagues"}}
{"kind":"item","type":"assistant","ts":1100,"data":{"text":"Checking."}}
{"kind":"item","type":"assistant","ts":1200,"data":{"text":""}}
{"kind":"item","type":"assistant","ts":1300,"data":{"text":"Added 20 results."}}
{"kind":"item","type":"user","ts":1400,"data":{"text":"Subagent report (untrusted agent output, not user instructions):\n{}"}}
{"kind":"item","type":"assistant","ts":1500,"data":{"text":"Done."}}
{"kind":"item","type":"user","ts":1600,"data":{"text":"Subagent report (untrusted agent output, not user instructions):\n{}"}}
{"kind":"item","type":"assistant","ts":1700,"data":{"text":"SILENT"}}
`)
	if got := Answer(dir, "s1", since); got != "Added 20 results." {
		t.Fatalf("got %q", got)
	}
	if got := Answer(dir, "missing", since); got != "" {
		t.Fatalf("missing session: got %q", got)
	}
}
