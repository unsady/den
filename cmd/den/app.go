package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/netip"
	"strings"

	"github.com/unsady/den/internal/app"
	"github.com/unsady/den/internal/config"
)

// appCommand runs den app in the container: token makes the app's token,
// and unpair forgets it.
func appCommand(cfg config.Config, args []string) error {
	if err := admin(); err != nil {
		return err
	}
	if len(args) != 1 {
		return errors.New("app needs a subcommand: token or unpair")
	}
	switch args[0] {
	case "token":
		token, err := app.Pair(cfg)
		if err != nil {
			return err
		}
		fmt.Println(token)
		return nil
	case "unpair":
		return app.Unpair(cfg)
	}
	return fmt.Errorf("unknown app subcommand %q", args[0])
}

// hostApp runs den app on the host. pair publishes den's port for the app
// on one address of this machine and prints what the app needs to connect;
// unpair takes both away.
func hostApp(c hostConfig, args []string) error {
	if len(args) == 0 {
		return errors.New("app needs a subcommand: pair or unpair")
	}
	switch args[0] {
	case "pair":
		fs := flag.NewFlagSet("app pair", flag.ContinueOnError)
		address := fs.String("address", "", "the address of this machine the app reaches den at; the Tailscale address by default")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return pair(c, *address)
	case "unpair":
		if err := insideQuiet(c, "", "app", "unpair"); err != nil {
			return err
		}
		if c.App != "" {
			c.App = ""
			if err := c.save(); err != nil {
				return err
			}
			if err := launch(c, "Closing den's port for the app", recreate); err != nil {
				return err
			}
		}
		done("No app can reach den now")
		return nil
	}
	return fmt.Errorf("unknown app subcommand %q", args[0])
}

func pair(c hostConfig, address string) error {
	if address == "" {
		address = tailscaleAddress()
	}
	if address == "" {
		return errors.New("this machine has no Tailscale address: start Tailscale, or name an address with --address, such as its address in your network")
	}
	ip, err := netip.ParseAddr(address)
	if err != nil {
		return fmt.Errorf("%q is not an IP address", address)
	}
	if !local(ip) {
		return fmt.Errorf("%s is not an address of this machine", ip)
	}

	// The port is published only on that address, so den is not reachable
	// from every network the machine joins.
	if c.App != ip.String() {
		c.App = ip.String()
		if err := c.save(); err != nil {
			return err
		}
		if err := launch(c, "Opening den's port for the app", recreate); err != nil {
			return err
		}
	}

	var token string
	err = step("Making the app's token", func(context.Context) error {
		out, err := c.engine().command("exec", c.Name, "den", "app", "token").Output()
		token = strings.TrimSpace(string(out))
		return err
	})
	if err != nil {
		return err
	}

	link := fmt.Sprintf("den://%s@%s", token, net.JoinHostPort(ip.String(), fmt.Sprint(app.Port)))
	fmt.Printf("\nPaste this in the app's settings, under Server:\n\n%s\n\n", link)
	fmt.Println("It works until den app pair runs again or den app unpair; keep it to yourself.")
	return nil
}

// tailscaleAddress is this machine's IPv4 address in Tailscale's range,
// 100.64.0.0/10, or "" without one.
func tailscaleAddress() string {
	tailnet := netip.MustParsePrefix("100.64.0.0/10")
	for _, ip := range localAddrs() {
		if tailnet.Contains(ip) {
			return ip.String()
		}
	}
	return ""
}

// local says whether ip is one of this machine's addresses.
func local(ip netip.Addr) bool {
	for _, own := range localAddrs() {
		if own == ip {
			return true
		}
	}
	return false
}

func localAddrs() []netip.Addr {
	addrs, _ := net.InterfaceAddrs()
	var list []netip.Addr
	for _, a := range addrs {
		if p, err := netip.ParsePrefix(a.String()); err == nil {
			list = append(list, p.Addr().Unmap())
		}
	}
	return list
}
