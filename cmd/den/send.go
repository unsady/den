package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/unsady/den/internal/client"
	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/daemon"
	"github.com/unsady/den/internal/notify"
	"github.com/unsady/den/internal/widget"
)

func quietCommand(cfg config.Config, args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("quiet takes no arguments")
	}
	session := os.Getenv("DEN_SESSION")
	if session == "" {
		return fmt.Errorf("quiet needs DEN_SESSION, which den sets for the agent's commands")
	}

	if err := client.New(cfg).Do(http.MethodPost, "/quiet", daemon.QuietRequest{Session: session}, nil); err != nil {
		return err
	}
	fmt.Println("quiet")
	return nil
}

func rememberedCommand(cfg config.Config, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("remembered takes one line in quotes")
	}
	session := os.Getenv("DEN_SESSION")
	if session == "" {
		return fmt.Errorf("remembered needs DEN_SESSION, which den sets for the agent's commands")
	}

	req := daemon.RememberedRequest{Session: session, Text: args[0]}
	if err := client.New(cfg).Do(http.MethodPost, "/remembered", req, nil); err != nil {
		return err
	}
	fmt.Println("noted")
	return nil
}

func sendCommand(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	caption := fs.String("caption", "", "text under the file")
	source := fs.String("source", "", "the http(s) page the file came from")
	widgetFile := fs.String("widget", "", "a JSON file that describes the file for the app to draw itself, such as a chart's data")
	rest, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return fmt.Errorf("send needs one file")
	}

	if err := daemon.CheckSource(*source); err != nil {
		return err
	}

	data, err := os.ReadFile(rest[0])
	if err != nil {
		return err
	}
	if len(data) > notify.MaxUpload {
		return fmt.Errorf("%s is over Telegram's 50 MB limit for bots", rest[0])
	}
	req := daemon.SendRequest{Name: filepath.Base(rest[0]), Caption: *caption, Source: *source, Data: data, Session: os.Getenv("DEN_SESSION")}
	if *widgetFile != "" {
		spec, err := os.ReadFile(*widgetFile)
		if err != nil {
			return err
		}
		if req.Widget, err = widget.Check(spec); err != nil {
			return err
		}
	}
	if err := client.New(cfg).Do(http.MethodPost, "/send", req, nil); err != nil {
		return err
	}
	fmt.Println(sent(data, req.Widget != nil))
	return nil
}

// sent says the file went, and reminds the agent, which reads it, that a
// picture of a chart goes with its data: a recipe of its own may have
// left the data out.
func sent(data []byte, widget bool) string {
	if widget || !strings.HasPrefix(http.DetectContentType(data), "image/") {
		return "sent"
	}
	return "sent. If it was a line or bar chart, send its data with --widget too next time, as the charts skill says, so the den app draws it."
}
