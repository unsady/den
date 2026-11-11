package chat

import "testing"

func TestStep(t *testing.T) {
	for _, c := range []struct{ line, want string }{
		{`Running ~/.local/bin/yt-dlp --print "%(title)s" "https://youtu.be/0KgNNcQNYTA?si=x"`, "Running yt-dlp · youtu.be"},
		{`Running : ; jq . a.json`, "Running jq"},
		{`Running den search -n 5 "lightpanda 1.0 release"`, "Searching “lightpanda 1.0 release”"},
		{`Running den stats --days 7`, "Running den stats"},
		{`Running n=$(jq '.sources | length' /tmp/x.json); echo $n`, "Running jq"},
		{`Running total=$(jq -s 'map(.in) | add' a.jsonl)`, "Running jq"},
		{`Running for f in /tmp/*.json; do jq '.a' "$f"; done`, "Running jq"},
		{`Running if jq -e '.ok' r.json >/dev/null; then echo ok; fi`, "Running jq"},
		{`Running { jq '.a' a.json; jq '.b' b.json; } > out`, "Running jq"},
		{`Running time yt-dlp -F x`, "Running yt-dlp"},
		{`Running echo '{"a":1}' | jq '.a'`, "Running jq"},
		{`Running '.[0]'`, "Running a command"},
		{`Running command -v yt-dlp && yt-dlp -F x`, "Running yt-dlp"},
		{"Waiting for jq --slurpfile catalog /tmp/check/catalog.json '.items[] | select(.a)' plan.json", "Waiting for jq"},
		{"Waiting for den fetch --secret news https://example.com/api", "Waiting for a command"},
		{"Waiting for the shell execution", "Waiting for a command"},
		{"Running set -e\nden fetch --secret news https://example.com/api", "Calling example.com"},
		{`Running sleep 5`, "Running sleep"},
		{`Running jq -r '.a | select(.b and .c) | .d; .e' f.json`, "Running jq"},
		{`Running printf 'a | b' ; sed -n 1p x`, "Running sed"},
		{`Running den service ls`, "Running den service"},
		{`Running den --help`, "Running den"},
		{`Running den fetch --secret github https://api.github.com/repos/me/notes/issues`, "Calling api.github.com"},
		{`Running cd notes && rg -i "invoice"`, "Running rg"},
		{`Running LANG=C sort list.txt`, "Running sort"},
		{`Running lightpanda fetch --dump markdown "https://www.react.dev/learn"`, "Running lightpanda · react.dev"},
		{`Running sleep 5 && ~/.local/bin/yt-dlp --list-subs "https://youtu.be/x"`, "Running yt-dlp · youtu.be"},
		{`Running den search "Qx0OgeG5wSw youtube video" 2>&1 | head`, "Searching “Qx0OgeG5wSw youtube video”"},
		{`Running den search -n 5 lightpanda release 2>&1 | head -20`, "Searching “lightpanda release”"},
		{`Running mkdir -p ~/media; cd ~/media && ffmpeg -i a.webm a.wav`, "Running ffmpeg"},
		{`Running sleep 30`, "Running sleep"},
		{"Reading /home/agent/space/notes/todo.md", "Reading todo.md"},
		{"Fetching https://www.example.com/page?x=1", "Reading example.com"},
		{"Searching current Zig release | allowed: ziglang.org", "Searching “current Zig release”"},
		{"Loading skill media", "Using the media skill"},
		{"Loading skill skill", "Using a skill"},
		{`Running den fetch --secret google "$g/messages?q=is:unread"`, "Calling Google"},
		{`Running g=https://gmail.googleapis.com/gmail/v1/users/me; den fetch --secret google "$g/profile"`, "Calling Google"},
		{`Running den fetch --secret google https://gmail.googleapis.com/gmail/v1/users/me/profile`, "Calling gmail.googleapis.com"},
		{`Running curl -s "imap://127.0.0.1:1143/INBOX" -u den:den -X 'UID SEARCH UNSEEN'`, "Checking mail"},
		{`Running m='imap://127.0.0.1:1143'; curl -s "$m/" -u den:den`, "Checking mail"},
		{`Running curl -s smtp://127.0.0.1:1025 --mail-rcpt a@b.c --upload-file mail.eml`, "Sending an email"},
		{"Editing AGENTS.md", "Editing AGENTS.md"},
	} {
		got, ok := step(c.line)
		if !ok || got != c.want {
			t.Errorf("step(%q) = %q, %v; want %q", c.line, got, ok, c.want)
		}
	}

	for _, line := range []string{
		"2026.10.26",
		"/dev/fd/0: line 10: yt-dlp: command not found",
		"WARNING: [youtube] No supported JavaScript runtime could be found.",
		"<div class=\"title\">Running late</div>",
		"Reading more command output",
	} {
		if got, ok := step(line); ok {
			t.Errorf("step(%q) = %q, want it dropped", line, got)
		}
	}
}

func TestTokens(t *testing.T) {
	for n, want := range map[int]string{512: "512", 38211: "38k", 400000: "400k", 1000000: "1M", 1260000: "1.3M"} {
		if got := tokens(n); got != want {
			t.Errorf("tokens(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestStepDone(t *testing.T) {
	for in, want := range map[string]string{
		"Running yt-dlp · youtu.be":     "Ran yt-dlp · youtu.be",
		"Reading notes.md":              "Read notes.md",
		"Searching “lightpanda 1.0”":    "Searched “lightpanda 1.0”",
		"Sending you a file":            "Sent you a file",
		"Using the github skill":        "Used the github skill",
		"Looking through past sessions": "Looked through past sessions",
		"Asking you":                    "Asked you",
		"Checking mail":                 "Checked mail",
		"Calling GitHub":                "Called GitHub",
		"Something else":                "Something else",
	} {
		if got := (stepGroup{text: in, count: 1}).done().text; got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}
