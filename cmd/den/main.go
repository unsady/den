// Command den is a small home for a personal fx agent: a scheduler,
// notifications, and an archive of past sessions.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	_ "time/tzdata"

	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/daemon"
	"github.com/unsady/den/internal/seed"
)

const usage = `den: a small home for a personal fx agent.

Usage:
  den job add --schedule <spec> [--name <name>] [--slot main|second] [--silent] <prompt>
  den job ls [--json]
  den job slot <id|name> main|second
                                Run the job on the chat's main or second model
  den job rm <id|name>
  den job run <id|name>
  den job log <id|name> [-n <count>] [--json]
  den quiet                     Send nothing for this turn: end it with this command
  den send [--caption <text>] <file>
                                Send the user a file; an image goes as a photo
  den fetch --secret <name> [-X <method>] [-H "<name>: <value>"]... [-d <body>|@<file>|@-] [-i] <url>
                                Send an HTTP request with a secret den holds,
                                such as github; prints the response body
  den search [-n <count>] [--json] <query>
                                Search the web through the services den has keys for
  den map -p <instructions>|@<file> [--model <provider/model>] [--out <file>] [--workers 6] [--resume] <items.jsonl>
                                Ask a cheap model about each line in its own short
                                request and write one JSON answer per item
  den sessions export [--force] Write fx sessions as markdown into the archive
  den sessions recent [--since 24h] [--after <file>]
                                Export, then list sessions that changed since then
  den sessions ls [-n <count>]  The latest sessions: id, turns, when, and title
  den sessions rm <id>...       Delete sessions, and the chat forgets them; an id
                                that starts with - goes after --
  den secret set <name> | ls | rm <name>
                                Keep a secret, read from stdin (root only)
  den stats [--since 24h] [--json]
                                How each model provider answered: silence before the
                                first token, stalls, speed, and failures
  den service ls                The APIs den sends a token to, for den fetch --secret
  den service add [--header <name>] [--ask writes|deletes|never|always] [--about <text>] <name> <https-url>
                                Add an API with its token on stdin (root only)
  den service rm <name>
  den login [--model <id>] [--base-url <url>] <provider>
                                Sign the agent in to a model provider with the key
                                on stdin, which stays with den (root only)
  den connect google | mail     Connect Google over stdio, or a mailbox from the
                                account on stdin (root only)
  den disconnect google | mail
  den app token | unpair        Make the app's token, which replaces the last one,
                                or forget it (root only)
  den serve                     Run the daemon: scheduler, notifications, and chat
  den init                      Create the agent's space if it is missing

Schedules:
  in 30m | in 2h               once, that long from now
  2026-11-10 09:00             once, at that local time
  every 2h                     repeatedly, at that interval
  0 9 * * 1-5 | @daily         repeatedly, by cron expression

A job runs fx in a fresh session in the space. Its final reply is sent to
the user unless the run ends with den quiet or the job is --silent.
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "den:", err)
		os.Exit(1)
	}
}

// host runs the commands of den on the host, which drive den in its
// container. It is nil elsewhere.
var host func(args []string) error

// version is den's release, set when a release is built.
var version = "dev"

func run(args []string) error {
	if len(args) == 1 && (args[0] == "version" || args[0] == "--version") {
		fmt.Println("den " + version)
		return nil
	}
	if host != nil {
		return host(args)
	}
	cfg := config.Load()
	if len(args) == 0 {
		fmt.Print(usage)
		return nil
	}

	switch args[0] {
	case "job", "jobs":
		return jobCommand(cfg, args[1:])
	case "quiet":
		return quietCommand(cfg, args[1:])
	case "send":
		return sendCommand(cfg, args[1:])
	case "fetch":
		return fetchCommand(cfg, args[1:])
	case "search":
		return searchCommand(cfg, args[1:])
	case "map":
		return mapCommand(cfg, args[1:])
	case "sessions":
		return sessionsCommand(cfg, args[1:])
	case "secret", "secrets":
		return secretCommand(cfg, args[1:])
	case "service", "services":
		return serviceCommand(cfg, args[1:])
	case "stats":
		return statsCommand(cfg, args[1:])
	case "login":
		return loginCommand(cfg, args[1:])
	case "connect":
		return connectCommand(cfg, args[1:])
	case "disconnect":
		return disconnectCommand(cfg, args[1:])
	case "app":
		return appCommand(cfg, args[1:])
	case "serve":
		return daemon.Serve(cfg)
	case "init":
		return seed.Init(cfg)
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	}
	return fmt.Errorf("unknown command %q; run `den help`", args[0])
}

// parse reads flags anywhere among args, so `den job log reflect -n 5`
// works. Everything after "--" is a positional argument.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		if consumed := len(args) - len(rest); consumed > 0 && args[consumed-1] == "--" {
			return append(positional, rest...), nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

func oneArg(command string, args []string) (string, error) {
	if len(args) != 1 {
		return "", fmt.Errorf("%s needs one job id or name", command)
	}
	return args[0], nil
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
