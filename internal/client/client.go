// Package client talks to the den daemon over its unix socket.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"syscall"
	"time"

	"github.com/unsady/den/internal/config"
)

type Client struct {
	socket string
	http   *http.Client
}

func New(cfg config.Config) *Client {
	var dialer net.Dialer
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", cfg.Socket)
		},
	}
	return &Client{socket: cfg.Socket, http: &http.Client{Transport: transport, Timeout: 2 * time.Minute}}
}

// Wait lets one call take up to d, for calls that wait for the user.
func (c *Client) Wait(d time.Duration) *Client {
	c.http.Timeout = d
	return c
}

// Do sends in as JSON and decodes the reply into out. Either may be nil.
func (c *Client) Do(method, path string, in, out any) error {
	var body bytes.Buffer
	if in != nil {
		if err := json.NewEncoder(&body).Encode(in); err != nil {
			return err
		}
	}

	req, err := http.NewRequest(method, "http://den"+path, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		if errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED) {
			return fmt.Errorf("the den daemon is not running (no socket at %s); start it with `den serve`", c.socket)
		}
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		if json.NewDecoder(resp.Body).Decode(&e) == nil && e.Error != "" {
			return errors.New(e.Error)
		}
		return fmt.Errorf("den daemon: HTTP %d", resp.StatusCode)
	}

	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
