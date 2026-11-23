package daemon

import (
	"context"
	"log"
	"net/http"
	"slices"
	"sort"
	"time"

	"github.com/unsady/den/internal/today"
)

// calendarClient reads the user's calendar for the Today card.
var calendarClient = &http.Client{Timeout: 10 * time.Second}

// today gathers the user's day for the app.
func (d *daemon) today(ctx context.Context) (today.Day, error) {
	now := time.Now()
	start, end := today.Bounds(now)
	day := today.Day{Date: start.Format(time.DateOnly), Events: []today.Event{}, Jobs: []today.Job{}, Night: []today.Result{}}

	tasks, version, err := today.Tasks(d.cfg)
	if err != nil {
		return today.Day{}, err
	}
	day.Tasks, day.Version = tasks, version

	list, err := d.store.List()
	if err != nil {
		return today.Day{}, err
	}
	titles := map[string]string{}
	for _, j := range list {
		titles[j.Name] = j.Title
		// A reminder is part of the day's plan, as an event is; a repeating
		// job is routine that tells nothing of the day.
		if !j.Once || j.NextRun.Before(now) || !j.NextRun.Before(end) {
			continue
		}
		day.Jobs = append(day.Jobs, today.Job{Name: j.Name, Title: j.Title, At: j.NextRun, Once: j.Once})
	}
	sort.SliceStable(day.Jobs, func(a, b int) bool { return day.Jobs[a].At.Before(day.Jobs[b].At) })

	// What came in the quiet hours arrived without a sound, so the card
	// shows it in the morning.
	from, to := today.Night(now)
	runs, err := d.db.RunsBetween(ctx, from, to)
	if err != nil {
		return today.Day{}, err
	}
	for _, run := range runs {
		if !run.Delivered || run.SessionID == "" {
			continue
		}
		title := titles[run.Job]
		if title == "" {
			title = run.Job
		}
		text := run.Output
		if run.Error != "" {
			text = run.Error
		}
		day.Night = append(day.Night, today.Result{
			Session: run.SessionID, Title: title, At: run.Started, Text: today.FirstLine(text), Failed: run.Error != "",
		})
	}

	// A calendar den cannot read leaves the rest of the day to show.
	if slices.Contains(d.google.Granted(), "Calendar") {
		events, err := d.calendar(ctx, start, end)
		if err != nil {
			log.Printf("today: calendar: %v", err)
			day.Calendar = err.Error()
		} else {
			day.Events = events
		}
	}
	return day, nil
}

func (d *daemon) calendar(ctx context.Context, from, to time.Time) ([]today.Event, error) {
	token, err := d.google.Token(ctx)
	if err != nil {
		return nil, err
	}
	return today.Events(ctx, calendarClient, token, from, to)
}
