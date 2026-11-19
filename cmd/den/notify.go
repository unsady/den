package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	_ "time/tzdata"

	"github.com/unsady/den/internal/client"
	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/daemon"
	"github.com/unsady/den/internal/notify"
	"github.com/unsady/den/internal/widget"
)

func notifyCommand(cfg config.Config, args []string) error {
	text := strings.Join(args, " ")
	if strings.TrimSpace(text) == "" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		text = string(data)
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("notify needs text, as arguments or on stdin")
	}

	if err := client.New(cfg).Do(http.MethodPost, "/notify", daemon.NotifyRequest{Text: text, Session: os.Getenv("DEN_SESSION")}, nil); err != nil {
		return err
	}
	fmt.Println("sent")
	return nil
}

func sendCommand(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	caption := fs.String("caption", "", "text under the file")
	widgetFile := fs.String("widget", "", "a JSON file that describes the file for the app to draw itself, such as a chart's data")
	rest, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return fmt.Errorf("send needs one file")
	}

	data, err := os.ReadFile(rest[0])
	if err != nil {
		return err
	}
	if len(data) > notify.MaxUpload {
		return fmt.Errorf("%s is over Telegram's 50 MB limit for bots", rest[0])
	}
	req := daemon.SendRequest{Name: filepath.Base(rest[0]), Caption: *caption, Data: data, Session: os.Getenv("DEN_SESSION")}
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
	fmt.Println("sent")
	return nil
}
