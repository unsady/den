package chat

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
)

// fx ask writes each step it takes to stderr, such as "Running ls -la" or
// "Reading notes/todo.md", and in between the output of the commands it
// runs. A chat needs the steps only, said briefly.

// stepVerbs start the lines fx writes for its steps.
var stepVerbs = []string{
	"Running ", "Reading ", "Writing ", "Editing ", "Searching ", "Fetching ",
	"Loading skill ", "Installing skill ", "Asking", "Waiting for ",
}

var (
	urlPattern = regexp.MustCompile(`https?://[^\s"'<>)]+`)
	// separators split a shell line into the commands it chains.
	separators = regexp.MustCompile(`\s*(?:&&|\|\||;|\||\n)\s*`)
	quotedText = regexp.MustCompile(`"([^"]+)"|'([^']+)'`)
	// programName is what a program's name looks like, unlike a jq filter
	// or a flag that the parsing below might land on.
	programName = regexp.MustCompile(`^[A-Za-z0-9_][\w.+-]*$`)
)

// keywords are the shell's own words before a command, as in
// "for f in *; do jq …" or "time yt-dlp …".
var keywords = map[string]bool{
	"for": true, "while": true, "until": true, "if": true, "then": true, "else": true,
	"elif": true, "do": true, "done": true, "fi": true, "time": true, "!": true,
	"{": true, "}": true, "(": true, ")": true, "exec": true, "command": true,
	"esac": true,
}

// helpers are programs that set the stage for the command that matters,
// such as the sleep in "sleep 5 && yt-dlp …".
var helpers = map[string]bool{
	"sleep": true, "cd": true, "export": true, "set": true, "mkdir": true,
	"true": true, ":": true, "source": true, ".": true, "umask": true,
	"echo": true, "printf": true,
}

// step turns a line fx wrote into a short step for the chat, or reports
// false when the line is not a step, such as a command's output.
func step(line string) (string, bool) {
	line = strings.TrimSpace(line)
	verb := ""
	for _, v := range stepVerbs {
		if strings.HasPrefix(line, v) {
			verb = v
			break
		}
	}
	if verb == "" {
		return "", false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(line, verb))

	switch verb {
	case "Running ":
		return shellStep(rest), true
	case "Waiting for ":
		// fx waits on a command that still runs in the background.
		if what, ok := strings.CutPrefix(shellStep(rest), "Running "); ok {
			return "Waiting for " + what, true
		}
		return "Waiting for a command", true
	case "Reading ":
		if strings.HasPrefix(rest, "more ") {
			return "", false
		}
		if strings.HasPrefix(rest, "skill resource ") {
			return "Reading the skill", true
		}
		return "Reading " + path.Base(rest), true
	case "Writing ", "Editing ":
		return strings.TrimSpace(verb) + " " + path.Base(rest), true
	case "Fetching ":
		return "Reading " + host(rest), true
	case "Searching ":
		query, _, _ := strings.Cut(rest, " | ")
		return "Searching “" + clipRunes(query, 60) + "”", true
	case "Loading skill ":
		// Before fx knows which skill a call loads, it names the tool itself.
		if rest == "" || rest == "skill" {
			return "Using a skill", true
		}
		return "Using the " + rest + " skill", true
	case "Installing skill ":
		return "Installing a skill", true
	case "Asking":
		return "Asking you", true
	}
	return clipRunes(line, 60), true
}

// shellStep says what a shell command does by the program it runs, and the
// site when it names a URL: "Running yt-dlp · youtu.be".
func shellStep(cmd string) string {
	line := cmd
	var fields []string
	var program string
	// The first command that is not a helper names the step; a line of
	// helpers alone is named by its last one, as in "sleep 5".
	var helper string
	var helperFields []string
	for _, part := range splitCommands(cmd) {
		words := commandFields(part)
		if len(words) == 0 {
			continue
		}
		name := path.Base(strings.Trim(words[0], `"'(`))
		if !programName.MatchString(name) {
			continue
		}
		if helpers[name] {
			helper, helperFields = name, words
			continue
		}
		program, fields, cmd = name, words, part
		break
	}
	if program == "" {
		program, fields = helper, helperFields
	}
	if program == "" {
		return "Running a command"
	}

	if program == "den" && len(fields) > 1 {
		switch fields[1] {
		case "search":
			return "Searching “" + clipRunes(searchQuery(cmd, fields[2:]), 60) + "”"
		case "fetch":
			return "Calling " + service(cmd, fields[2:])
		case "send":
			return "Sending you a file"
		case "notify":
			return "Sending you a message"
		case "job":
			return "Managing jobs"
		case "sessions":
			return "Looking through past sessions"
		}
		// Other den commands say which one runs: "Running den stats".
		if !strings.HasPrefix(fields[1], "-") {
			return "Running den " + clipRunes(strings.Trim(fields[1], `"'`), 20)
		}
	}

	// den serves the user's mailbox on the loopback address.
	switch {
	case strings.Contains(line, "imap://127.0.0.1"):
		return "Checking mail"
	case strings.Contains(line, "smtp://127.0.0.1"):
		return "Sending an email"
	}

	text := "Running " + program
	if h := host(cmd); h != "" {
		text += " · " + h
	}
	return text
}

// splitCommands splits a shell line into the commands it chains, leaving
// separators in quotes alone, such as the pipes of a jq filter.
func splitCommands(line string) []string {
	var masked strings.Builder
	var quote rune
	for _, r := range line {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote == 0 && (r == '\'' || r == '"'):
			quote = r
		case quote != 0 && strings.ContainsRune("|&;\n", r):
			r += hidden
		}
		masked.WriteRune(r)
	}

	parts := separators.Split(masked.String(), -1)
	for i, part := range parts {
		parts[i] = strings.Map(func(r rune) rune {
			if r >= hidden {
				return r - hidden
			}
			return r
		}, part)
	}
	return parts
}

// hidden moves a separator in quotes out of the separators' way, into
// Unicode's private use planes.
const hidden = 0xF0000

// commandFields splits one command of a shell line into words from its
// program on: past the shell's keywords and variable assignments, and into
// a command substitution, as in "n=$(jq …)".
func commandFields(part string) []string {
	fields := strings.Fields(part)
	// "for f in *" names no program; the command follows its "do".
	if len(fields) > 0 && (fields[0] == "for" || fields[0] == "case" || fields[0] == "select") {
		return nil
	}
	for len(fields) > 0 {
		word := fields[0]
		if _, inner, ok := strings.Cut(word, "$("); ok && inner != "" {
			fields[0] = inner
			return fields
		}
		if keywords[word] || strings.Contains(word, "=") && !strings.HasPrefix(word, "-") {
			fields = fields[1:]
			continue
		}
		return fields
	}
	return nil
}

// services name the secrets of den fetch for the user.
var services = map[string]string{"github": "GitHub", "google": "Google"}

// service says what a den fetch calls: the site of its URL, or the service
// of its secret when the URL is in a variable.
func service(cmd string, args []string) string {
	if h := host(cmd); h != "" {
		return h
	}
	for i, arg := range args {
		name, ok := strings.CutPrefix(arg, "--secret=")
		if arg == "--secret" && i+1 < len(args) {
			name, ok = args[i+1], true
		}
		if ok {
			name = strings.Trim(name, `"'`)
			if label, known := services[name]; known {
				return label
			}
			return name
		}
	}
	return "a service"
}

// searchQuery finds the query of a den search: the quoted text, or else the
// words that are neither flags nor redirections.
func searchQuery(cmd string, args []string) string {
	if m := quotedText.FindStringSubmatch(cmd); m != nil {
		return m[1] + m[2]
	}
	var words []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-n":
			i++
		case strings.HasPrefix(args[i], "-"), strings.ContainsAny(args[i], "<>"):
		default:
			words = append(words, args[i])
		}
	}
	return strings.Join(words, " ")
}

// host returns the site of the first URL in text, or "" when there is none.
func host(text string) string {
	u, err := url.Parse(urlPattern.FindString(text))
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.TrimPrefix(u.Hostname(), "www.")
}

// stepGroup is a step and how many times in a row it came.
type stepGroup struct {
	text  string
	count int
}

func (g stepGroup) String() string {
	if g.count > 1 {
		return fmt.Sprintf("%s ×%d", g.text, g.count)
	}
	return g.text
}

// pastTense is each step's verb once the step is over, as fx's own
// transcript says "Ran pwd" after "Running pwd".
var pastTense = [][2]string{
	{"Running ", "Ran "}, {"Reading ", "Read "}, {"Writing ", "Wrote "},
	{"Editing ", "Edited "}, {"Searching ", "Searched "}, {"Calling ", "Called "},
	{"Sending ", "Sent "}, {"Managing ", "Managed "}, {"Looking ", "Looked "},
	{"Checking ", "Checked "}, {"Using ", "Used "}, {"Installing ", "Installed "},
	{"Asking ", "Asked "},
}

// done says a step in the past tense.
func (g stepGroup) done() stepGroup {
	for _, p := range pastTense {
		if rest, ok := strings.CutPrefix(g.text, p[0]); ok {
			g.text = p[1] + rest
			return g
		}
	}
	return g
}
