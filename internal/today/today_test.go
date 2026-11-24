package today

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsady/den/internal/config"
)

const list = "# Today\n\n- [ ] 15:00 Interview\n- [ ] 25:00 Not a time\n- [ ] Buy oat milk\n- [x] Call the bank\n  * [ ] Ask about the card\n\n```\n- [ ] not a to-do\n```\n- [ ]   \nSome text.\n"

func TestTasks(t *testing.T) {
	cfg := config.Config{Space: t.TempDir()}
	tasks, err := Tasks(cfg)
	if err != nil || len(tasks) != 0 {
		t.Fatalf("no list: %v %v", tasks, err)
	}

	if err := os.WriteFile(filepath.Join(cfg.Space, File), []byte(list), 0o644); err != nil {
		t.Fatal(err)
	}
	tasks, err = Tasks(cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := []Task{{"15:00", "Interview", false}, {"", "25:00 Not a time", false}, {"", "Buy oat milk", false}, {"", "Call the bank", true}, {"", "Ask about the card", false}}
	if len(tasks) != len(want) {
		t.Fatalf("Tasks = %+v", tasks)
	}
	for i := range want {
		if tasks[i] != want[i] {
			t.Errorf("task %d = %+v, want %+v", i, tasks[i], want[i])
		}
	}
}

func TestCheck(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	space := t.TempDir()
	git := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = space
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return string(out)
	}
	git("init", "-q", "-b", "main")
	git("config", "user.name", "den")
	git("config", "user.email", "den@localhost")
	path := filepath.Join(space, File)
	if err := os.WriteFile(path, []byte(list), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-q", "-m", "Plan the day")
	cfg := config.Config{Space: space}

	if err := Check(cfg, 4, "Ask about the card", true); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if want := "# Today\n\n- [ ] 15:00 Interview\n- [ ] 25:00 Not a time\n- [ ] Buy oat milk\n- [x] Call the bank\n  * [x] Ask about the card\n\n```\n- [ ] not a to-do\n```\n- [ ]   \nSome text.\n"; string(data) != want {
		t.Fatalf("file = %q", data)
	}
	if got := git("log", "-1", "--name-only", "--format=%s"); got != "Tick a to-do in the app\n\ntoday.md\n" {
		t.Errorf("commit = %q", got)
	}

	// The agent moved the to-dos: the app's index no longer finds its text.
	if err := Check(cfg, 0, "Call the bank", false); !errors.Is(err, ErrChanged) {
		t.Errorf("moved to-do: %v", err)
	}
	if err := Check(cfg, 5, "Water the plants", true); !errors.Is(err, ErrChanged) {
		t.Errorf("past the list: %v", err)
	}
	if err := Check(cfg, 3, "Call the bank", false); err != nil {
		t.Fatal(err)
	}
	if tasks, _ := Tasks(cfg); tasks[3].Done {
		t.Error("the bank stayed ticked")
	}

	// A tick names the to-do by its text alone; its time stays in the file.
	if err := Check(cfg, 0, "Interview", true); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); !strings.HasPrefix(string(data), "# Today\n\n- [x] 15:00 Interview\n") {
		t.Errorf("file = %q", data)
	}
}
