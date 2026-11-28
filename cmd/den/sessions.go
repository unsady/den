package main

import (
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"text/tabwriter"
	"time"

	"github.com/unsady/den/internal/client"
	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/sessions"
)

func sessionsCommand(cfg config.Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("sessions needs a subcommand: export, recent, ls, rm, archive, or unarchive")
	}

	switch args[0] {
	case "export":
		fs := flag.NewFlagSet("sessions export", flag.ContinueOnError)
		force := fs.Bool("force", false, "rewrite sessions that have not changed")
		if _, err := parse(fs, args[1:]); err != nil {
			return err
		}

		stats, err := sessions.Export(cfg.Sessions, cfg.Archive, *force)
		if err != nil {
			return err
		}
		fmt.Printf("exported %d sessions to %s (%d unchanged, %d empty)\n", stats.Written, cfg.Archive, stats.Unchanged, stats.Skipped)
		return nil

	case "recent":
		fs := flag.NewFlagSet("sessions recent", flag.ContinueOnError)
		since := fs.Duration("since", 24*time.Hour, "how far back to look")
		after := fs.String("after", "", "list sessions changed after this file's modification time")
		if _, err := parse(fs, args[1:]); err != nil {
			return err
		}

		cutoff := time.Now().Add(-*since)
		if *after != "" {
			if info, err := os.Stat(*after); err == nil {
				cutoff = info.ModTime()
			}
		}

		if _, err := sessions.Export(cfg.Sessions, cfg.Archive, false); err != nil {
			return err
		}
		// The agent that asks, such as the reflect job's, is not one to read.
		paths, err := sessions.Recent(cfg.Sessions, cfg.Archive, cutoff, os.Getenv("DEN_SESSION"))
		if err != nil {
			return err
		}
		for _, p := range paths {
			fmt.Println(p)
		}
		return nil
	case "ls":
		fs := flag.NewFlagSet("sessions ls", flag.ContinueOnError)
		count := fs.Int("n", 30, "how many sessions to list")
		if _, err := parse(fs, args[1:]); err != nil {
			return err
		}

		list, err := sessions.List(cfg.Sessions, *count)
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		for _, s := range list {
			fmt.Fprintf(w, "%s\t%d\t%s\t%s\n", s.ID, s.Turns, s.Updated.Local().Format("2006-01-02 15:04"), s.Title)
		}
		return w.Flush()

	case "rm":
		// An id can start with -, so every argument is one, except a bare --.
		ids := slices.DeleteFunc(slices.Clone(args[1:]), func(a string) bool { return a == "--" })
		if len(ids) == 0 {
			return fmt.Errorf("sessions rm needs a session id")
		}

		c := client.New(cfg)
		failed := false
		for _, id := range ids {
			if err := c.Do(http.MethodDelete, "/sessions/"+url.PathEscape(id), nil, nil); err != nil {
				fmt.Fprintf(os.Stderr, "den: session %s: %v\n", id, err)
				failed = true
				continue
			}
			fmt.Printf("deleted session %s\n", id)
		}
		if failed {
			return fmt.Errorf("not every session was deleted")
		}
		return nil

	case "archive", "unarchive":
		ids := slices.DeleteFunc(slices.Clone(args[1:]), func(a string) bool { return a == "--" })
		if len(ids) == 0 {
			return fmt.Errorf("sessions %s needs a session id", args[0])
		}

		method, done := http.MethodPost, "archived"
		if args[0] == "unarchive" {
			method, done = http.MethodDelete, "unarchived"
		}
		c := client.New(cfg)
		failed := false
		for _, id := range ids {
			if err := c.Do(method, "/sessions/"+url.PathEscape(id)+"/archive", nil, nil); err != nil {
				fmt.Fprintf(os.Stderr, "den: session %s: %v\n", id, err)
				failed = true
				continue
			}
			fmt.Printf("%s session %s\n", done, id)
		}
		if failed {
			return fmt.Errorf("not every session was %s", done)
		}
		return nil
	}
	return fmt.Errorf("unknown sessions subcommand %q", args[0])
}
