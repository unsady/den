package memory

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsady/den/internal/config"
)

const seeded = `# Memory

These sections are loaded into every session.

## Soul

Name: Den.

## User

Lives in New York.
Prefers short replies.

## Notes

- Notes live in ~/space/notes.
`

func TestSection(t *testing.T) {
	if got := section(seeded, "User"); got != "Lives in New York.\nPrefers short replies." {
		t.Errorf("User = %q", got)
	}
	if got := section(seeded, "Notes"); got != "- Notes live in ~/space/notes." {
		t.Errorf("Notes = %q", got)
	}
	if got := section("# Memory\n", "Soul"); got != "" {
		t.Errorf("missing Soul = %q", got)
	}
}

func TestPut(t *testing.T) {
	tests := []struct {
		name, text, section, body, want string
	}{
		{
			name: "middle", text: seeded, section: "User", body: "Lives in Boston.\n",
			want: strings.Replace(seeded, "Lives in New York.\nPrefers short replies.", "Lives in Boston.", 1),
		},
		{
			name: "last", text: seeded, section: "Notes", body: "- One.",
			want: strings.Replace(seeded, "- Notes live in ~/space/notes.", "- One.", 1),
		},
		{
			name: "emptied", text: seeded, section: "Soul", body: "  ",
			want: strings.Replace(seeded, "## Soul\n\nName: Den.\n\n", "## Soul\n\n", 1),
		},
		{
			name: "missing goes before the next", text: "# Memory\n\n## User\n\nx\n", section: "Soul", body: "Den.",
			want: "# Memory\n\n## Soul\n\nDen.\n\n## User\n\nx\n",
		},
		{
			name: "missing goes last", text: "# Memory\n\n## User\n\nx\n", section: "Notes", body: "n",
			want: "# Memory\n\n## User\n\nx\n\n## Notes\n\nn\n",
		},
		{
			name: "own sections kept", text: "## User\n\nx\n\n## Places\n\np\n", section: "User", body: "y",
			want: "## User\n\ny\n\n## Places\n\np\n",
		},
		{
			name: "code block", text: "## Notes\n\n```\n# a comment\n```\n\n## Places\n\np\n", section: "Notes", body: "n",
			want: "## Notes\n\nn\n\n## Places\n\np\n",
		},
		{
			name: "indent kept", text: "## User\n\nx\n", section: "User", body: "\n  - y\n",
			want: "## User\n\n  - y\n",
		},
		{
			name: "heading in the body stays in it", text: "## Notes\n\nx\n", section: "Notes", body: "- a\n## Tools\n- b\n```\n# kept\n```",
			want: "## Notes\n\n- a\n### Tools\n- b\n```\n# kept\n```\n",
		},
		{
			name: "no file", text: "", section: "User", body: "x",
			want: "## User\n\nx\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := put(tt.text, tt.section, tt.body); got != tt.want {
				t.Errorf("put = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReadWrite(t *testing.T) {
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
	if err := os.WriteFile(path, []byte(seeded), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-q", "-m", "Create the space")
	// The agent's own change elsewhere stays out of den's commit.
	if err := os.WriteFile(filepath.Join(space, "today.md"), []byte("- [ ] x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Space: space}

	m, err := Read(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if m.Soul != "Name: Den." || m.Version == "" {
		t.Fatalf("Read = %+v", m)
	}

	m.User = "Lives in Boston."
	if err := Write(cfg, m); err != nil {
		t.Fatal(err)
	}
	if got, _ := Read(cfg); got.User != "Lives in Boston." || got.Soul != "Name: Den." {
		t.Errorf("after Write = %+v", got)
	}
	if got := git("log", "-1", "--name-only", "--format=%s"); got != "Edit memory in the app\n\nAGENTS.md\n" {
		t.Errorf("last commit = %q", got)
	}
	if got := git("status", "--porcelain"); got != "?? today.md\n" {
		t.Errorf("status = %q", got)
	}

	// m's version is the old file's now.
	if err := Write(cfg, m); !errors.Is(err, ErrChanged) {
		t.Errorf("stale Write = %v, want ErrChanged", err)
	}
}
