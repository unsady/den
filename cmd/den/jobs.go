package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/unsady/den/internal/client"
	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/daemon"
	"github.com/unsady/den/internal/jobs"
)

func jobCommand(cfg config.Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("job needs a subcommand: add, edit, ls, slot, rm, run, or log")
	}
	c := client.New(cfg)
	sub, args := args[0], args[1:]

	switch sub {
	case "add":
		fs := flag.NewFlagSet("job add", flag.ContinueOnError)
		var req daemon.AddRequest
		fs.StringVar(&req.Schedule, "schedule", "", "when the job runs")
		fs.StringVar(&req.Name, "name", "", "the job's name: 2 or 3 lowercase English words joined by -, such as morning-brief")
		fs.StringVar(&req.Title, "title", "", "the job's title in the user's language, which the user reads")
		fs.StringVar(&req.Slot, "slot", jobs.Main, "the chat's model the job runs on: main or second")
		fs.BoolVar(&req.Silent, "silent", false, "keep the reply in the job log instead of sending it")
		rest, err := parse(fs, args)
		if err != nil {
			return err
		}
		if req.Schedule == "" || req.Name == "" || req.Title == "" {
			return fmt.Errorf("job add needs --name, --title, and --schedule")
		}
		req.Prompt = strings.Join(rest, " ")

		var j jobs.Job
		if err := c.Do(http.MethodPost, "/jobs", req, &j); err != nil {
			return err
		}
		fmt.Printf("added job %s, next run %s\n", j.Name, when(j.NextRun))
		return nil

	case "edit":
		fs := flag.NewFlagSet("job edit", flag.ContinueOnError)
		title := fs.String("title", "", "the job's new title in the user's language")
		sched := fs.String("schedule", "", "when the job runs from now on")
		slot := fs.String("slot", "", "the chat's model the job runs on: main or second")
		// A string, so that --silent false never takes false for the prompt.
		silent := fs.String("silent", "", "true keeps the reply in the job log instead of sending it, false sends it")
		rest, err := parse(fs, args)
		if err != nil {
			return err
		}
		if len(rest) == 0 {
			return fmt.Errorf("job edit needs the job's name")
		}

		// Only what is given changes; words after the name are the new prompt.
		var change jobs.Change
		fs.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "title":
				change.Title = title
			case "schedule":
				change.Schedule = sched
			case "slot":
				change.Slot = slot
			case "silent":
				if quiet, err := strconv.ParseBool(*silent); err == nil {
					change.Silent = &quiet
				}
			}
		})
		if *silent != "" && change.Silent == nil {
			return fmt.Errorf("job edit takes --silent true or false, not %q", *silent)
		}
		if len(rest) > 1 {
			prompt := strings.Join(rest[1:], " ")
			change.Prompt = &prompt
		}
		if change == (jobs.Change{}) {
			return fmt.Errorf("job edit needs --title, --schedule, --slot, --silent, or a new prompt")
		}

		var j jobs.Job
		if err := c.Do(http.MethodPatch, "/jobs/"+url.PathEscape(rest[0]), change, &j); err != nil {
			return err
		}
		fmt.Printf("edited job %s, next run %s\n", j.Name, when(j.NextRun))
		return nil

	case "ls", "list":
		fs := flag.NewFlagSet("job ls", flag.ContinueOnError)
		asJSON := fs.Bool("json", false, "print JSON")
		if _, err := parse(fs, args); err != nil {
			return err
		}

		var list []jobs.Job
		if err := c.Do(http.MethodGet, "/jobs", nil, &list); err != nil {
			return err
		}
		if *asJSON {
			return printJSON(list)
		}
		if len(list) == 0 {
			fmt.Println("no jobs")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tTITLE\tNEXT\tSCHEDULE\tMODEL\tLAST\tPROMPT")
		for _, j := range list {
			last := "-"
			if j.LastRun != nil {
				last = j.LastStatus + " " + when(*j.LastRun)
			}
			slot := jobs.Main
			if j.Slot != "" {
				slot = j.Slot
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", j.Name, j.Title, when(j.NextRun), j.Schedule, slot, last, oneLine(j.Prompt, 50))
		}
		return w.Flush()

	case "slot":
		if len(args) != 2 {
			return fmt.Errorf("job slot takes a job and main or second")
		}
		var j jobs.Job
		if err := c.Do(http.MethodPatch, "/jobs/"+url.PathEscape(args[0]), jobs.Change{Slot: &args[1]}, &j); err != nil {
			return err
		}
		fmt.Printf("job %s runs on the %s model\n", j.Name, args[1])
		return nil

	case "rm", "remove":
		ref, err := oneArg("job rm", args)
		if err != nil {
			return err
		}
		var j jobs.Job
		if err := c.Do(http.MethodDelete, "/jobs/"+url.PathEscape(ref), nil, &j); err != nil {
			return err
		}
		fmt.Printf("removed job %s\n", j.Name)
		return nil

	case "run":
		ref, err := oneArg("job run", args)
		if err != nil {
			return err
		}
		var j jobs.Job
		if err := c.Do(http.MethodPost, "/jobs/"+url.PathEscape(ref)+"/run", nil, &j); err != nil {
			return err
		}
		fmt.Printf("started job %s; see `den job log %s`\n", j.Name, j.Name)
		return nil

	case "log":
		fs := flag.NewFlagSet("job log", flag.ContinueOnError)
		n := fs.Int("n", 3, "how many runs to show")
		asJSON := fs.Bool("json", false, "print the runs as JSON")
		rest, err := parse(fs, args)
		if err != nil {
			return err
		}
		ref, err := oneArg("job log", rest)
		if err != nil {
			return err
		}

		var runs []jobs.Run
		if err := c.Do(http.MethodGet, fmt.Sprintf("/jobs/%s/runs?n=%d", url.PathEscape(ref), *n), nil, &runs); err != nil {
			return err
		}
		if *asJSON {
			return json.NewEncoder(os.Stdout).Encode(runs)
		}
		if len(runs) == 0 {
			fmt.Println("no runs yet")
			return nil
		}
		for _, r := range runs {
			printRun(r)
		}
		return nil
	}
	return fmt.Errorf("unknown job subcommand %q", sub)
}

func printRun(r jobs.Run) {
	fmt.Printf("%s  %s  %s", r.Started.Local().Format("2006-01-02 15:04"), r.Status(), r.Duration)
	if r.Model != "" {
		fmt.Printf("  %s", r.Model)
	}
	if r.SessionID != "" {
		fmt.Printf("  session %s", r.SessionID)
	}
	if r.Delivered {
		fmt.Print("  delivered")
	}
	fmt.Println()

	if r.Output != "" {
		fmt.Println(indent(r.Output))
	}
	if r.Error != "" {
		fmt.Println(indent("error: " + r.Error))
		if r.Stderr != "" {
			fmt.Println(indent(r.Stderr))
		}
	}
	fmt.Println()
}

func when(t time.Time) string {
	return t.Local().Format("2006-01-02 15:04")
}

func oneLine(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > limit {
		return string(r[:limit-1]) + "…"
	}
	return s
}

func indent(s string) string {
	return "  " + strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n  ")
}
