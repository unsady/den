package today

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// calendarURL is where den reads the primary calendar's events.
var calendarURL = "https://www.googleapis.com/calendar/v3/calendars/primary/events"

// maxEvents is the most events a day shows.
const maxEvents = 50

// Events reads the events of the user's primary calendar that overlap from
// to to, with the access token, in start order. Cancelled events and those
// the user declined are left out.
func Events(ctx context.Context, client *http.Client, token string, from, to time.Time) ([]Event, error) {
	q := url.Values{
		"timeMin":      {from.Format(time.RFC3339)},
		"timeMax":      {to.Format(time.RFC3339)},
		"singleEvents": {"true"},
		"orderBy":      {"startTime"},
		"maxResults":   {fmt.Sprint(maxEvents)},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, calendarURL+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("Google Calendar answered %d: %s", resp.StatusCode, body)
	}

	var page struct {
		Items []struct {
			Summary   string `json:"summary"`
			Status    string `json:"status"`
			Start     when   `json:"start"`
			End       when   `json:"end"`
			Attendees []struct {
				Self           bool   `json:"self"`
				ResponseStatus string `json:"responseStatus"`
			} `json:"attendees"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return nil, err
	}

	events := []Event{}
	for _, item := range page.Items {
		declined := false
		for _, a := range item.Attendees {
			declined = declined || a.Self && a.ResponseStatus == "declined"
		}
		if item.Status == "cancelled" || declined {
			continue
		}

		start, allDay, err := item.Start.time(from.Location())
		if err != nil {
			return nil, err
		}
		end, _, err := item.End.time(from.Location())
		if err != nil {
			return nil, err
		}
		title := item.Summary
		if title == "" {
			title = "(no title)"
		}
		events = append(events, Event{Title: title, Start: start, End: end, AllDay: allDay})
	}
	return events, nil
}

// when is the start or end of an event: a date for an all-day event, a
// time for the rest.
type when struct {
	Date     string `json:"date"`
	DateTime string `json:"dateTime"`
}

func (w when) time(loc *time.Location) (time.Time, bool, error) {
	if w.Date != "" {
		t, err := time.ParseInLocation(time.DateOnly, w.Date, loc)
		return t, true, err
	}
	t, err := time.Parse(time.RFC3339, w.DateTime)
	return t.In(loc), false, err
}
