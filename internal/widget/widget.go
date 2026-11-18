// Package widget checks what the agent describes for the app to draw
// itself, such as a chart, in place of the picture Telegram shows.
//
// A chart is JSON:
//
//	{
//	  "type": "chart",
//	  "kind": "line",
//	  "title": "Expenses fell in March",
//	  "unit": "€",
//	  "labels": ["Jan", "Feb", "Mar"],
//	  "series": [{"name": "Food", "values": [420, 380, 310]}]
//	}
//
// kind is line, bar, or hbar (horizontal bars); unit is optional and goes
// after each value. Every series has a value for each label.
package widget

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
)

// MaxSize is the most a widget may weigh: it travels with every event and
// is kept in the session's journal.
const MaxSize = 32 << 10

// Limits that keep a chart readable on a phone.
const (
	maxSeries = 6
	maxPoints = 60
)

// Chart is a chart widget.
type Chart struct {
	Type   string   `json:"type"`
	Kind   string   `json:"kind"`
	Title  string   `json:"title"`
	Unit   string   `json:"unit,omitempty"`
	Labels []string `json:"labels"`
	Series []Series `json:"series"`
}

type Series struct {
	Name   string    `json:"name"`
	Values []float64 `json:"values"`
}

// Check reads data as a widget and returns it compact, or says what is
// wrong with it.
func Check(data []byte) (json.RawMessage, error) {
	if len(data) > MaxSize {
		return nil, fmt.Errorf("the widget is over %d KB", MaxSize>>10)
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var c Chart
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("the widget is not valid: %w", err)
	}

	if c.Type != "chart" {
		return nil, fmt.Errorf("the widget's type is chart, not %q", c.Type)
	}
	switch c.Kind {
	case "line", "bar", "hbar":
	default:
		return nil, fmt.Errorf("a chart's kind is line, bar, or hbar, not %q", c.Kind)
	}
	if len(c.Labels) == 0 || len(c.Labels) > maxPoints {
		return nil, fmt.Errorf("a chart has 1 to %d labels, not %d", maxPoints, len(c.Labels))
	}
	if len(c.Series) == 0 || len(c.Series) > maxSeries {
		return nil, fmt.Errorf("a chart has 1 to %d series, not %d", maxSeries, len(c.Series))
	}
	for _, s := range c.Series {
		if len(s.Values) != len(c.Labels) {
			return nil, fmt.Errorf("series %q has %d values for %d labels", s.Name, len(s.Values), len(c.Labels))
		}
		for _, v := range s.Values {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				return nil, errors.New("a chart's values are finite numbers")
			}
		}
	}

	return json.Marshal(c)
}
