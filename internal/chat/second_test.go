package chat

import (
	"context"
	"testing"

	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/jobs"
	"github.com/unsady/den/internal/runner"
)

func TestChoice(t *testing.T) {
	cfg := config.Config{State: t.TempDir()}
	c := New(cfg, nil, nil, testStore(t, cfg))
	main := runner.Choice{Provider: "p1", Model: "fast", Effort: "low"}
	c.setMainModel(main)

	// Without a second model, jobs on it run on the main one.
	if got := c.Choice(context.Background(), jobs.Second); got != main {
		t.Fatalf("second while unset = %+v, want %+v", got, main)
	}

	strong := runner.Choice{Provider: "p2", Model: "strong", Effort: "high"}
	c.setSecond(&strong)
	if got := c.Choice(context.Background(), jobs.Second); got != strong {
		t.Fatalf("second = %+v, want %+v", got, strong)
	}
	if got := c.Choice(context.Background(), ""); got != main {
		t.Fatalf("main = %+v, want %+v", got, main)
	}
}
