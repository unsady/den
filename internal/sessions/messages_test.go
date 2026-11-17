package sessions

import (
	"path/filepath"
	"testing"
	"time"
)

func TestMessages(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "s1", "log.jsonl"), `{"kind":"session_created","ts":1}
{"kind":"item","type":"user","ts":100,"data":{"text":"Plan my week"}}
{"kind":"item","type":"assistant","ts":110,"data":{"text":"Looking at the calendar."}}
{"kind":"item","type":"tool_call","ts":120,"data":{"tool_name":"bash"}}
{"kind":"item","type":"assistant","ts":130,"data":{"text":"Here is the plan."}}
{"kind":"item","type":"user","ts":200,"data":{"text":"Subagent report: done"}}
{"kind":"item","type":"assistant","ts":210,"data":{"text":"Done."}}
{"kind":"item","type":"user","ts":300,"data":{"text":"[den: the user allowed the push]"}}
{"kind":"item","type":"assistant","ts":310,"data":{"text":"Pushed."}}
{"kind":"item","type":"user","ts":400,"data":{"text":"Thanks"}}
`)

	list, err := Messages(dir, "s1")
	if err != nil {
		t.Fatal(err)
	}
	want := []Message{{true, "Plan my week", ms(100)}, {false, "Here is the plan.", ms(130)}, {false, "Pushed.", ms(310)}, {true, "Thanks", ms(400)}}
	if len(list) != len(want) {
		t.Fatalf("got %+v", list)
	}
	for i := range want {
		if list[i] != want[i] {
			t.Errorf("message %d = %+v, want %+v", i, list[i], want[i])
		}
	}

	if _, err := Messages(dir, "missing"); err == nil {
		t.Fatal("a missing session has no error")
	}
}

func ms(n int64) time.Time { return time.UnixMilli(n) }
