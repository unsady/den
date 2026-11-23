package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"

	"github.com/unsady/den/internal/backup"
	"github.com/unsady/den/internal/config"
)

// backupCommand is den backup in the container: take a snapshot now, list
// them, or unpack one into an empty den, as the host's restore does.
func backupCommand(cfg config.Config, args []string) error {
	if err := admin(); err != nil {
		return err
	}

	switch {
	case len(args) == 0:
		name, err := backup.Snapshot(context.Background(), cfg, time.Now())
		if name != "" {
			fmt.Println(name)
		}
		return err
	case args[0] == "ls" || args[0] == "list":
		local, remote, err := backup.List(context.Background(), cfg)
		for _, name := range local {
			fmt.Println("folder  " + name)
		}
		for _, name := range remote {
			fmt.Println("S3      " + name)
		}
		return err
	case args[0] == "unpack":
		return backup.Unpack(cfg, os.Stdin)
	}
	return fmt.Errorf("unknown backup subcommand %q", args[0])
}

// backupKeyPath is the private key that opens den's backups. It stays on
// the host, out of every folder the container sees.
func backupKeyPath() string { return filepath.Join(filepath.Dir(configPath()), "backup-key.txt") }

func defaultBackups() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "den", "backups")
}

// hostBackup is den backup on the host. unpack stays in the container:
// only restore runs it, while den is stopped.
func hostBackup(c hostConfig, args []string) error {
	if len(args) == 0 {
		return inside(c, "", "backup")
	}
	switch args[0] {
	case "setup":
		return backupSetup(c)
	case "restore":
		return backupRestore(c, args[1:])
	case "ls", "list":
		return inside(c, "", "backup", "ls")
	}
	return fmt.Errorf("unknown backup subcommand %q; run den help", args[0])
}

// backupKey reads the backups' key, or makes one when there is none, and
// reports whether it made it.
func backupKey() (*age.X25519Identity, bool, error) {
	data, err := os.ReadFile(backupKeyPath())
	if err == nil {
		ids, err := age.ParseIdentities(bytes.NewReader(data))
		if err != nil {
			return nil, false, fmt.Errorf("%s: %w", backupKeyPath(), err)
		}
		if id, ok := ids[0].(*age.X25519Identity); ok {
			return id, false, nil
		}
		return nil, false, fmt.Errorf("%s holds no X25519 key", backupKeyPath())
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, false, err
	}

	id, err := age.GenerateX25519Identity()
	if err != nil {
		return nil, false, err
	}
	text := fmt.Sprintf("# den's backup key, made %s\n# public key: %s\n%s\n",
		time.Now().Format(time.DateOnly), id.Recipient(), id)
	if err := os.MkdirAll(filepath.Dir(backupKeyPath()), 0o755); err != nil {
		return nil, false, err
	}
	if err := os.WriteFile(backupKeyPath(), []byte(text), 0o600); err != nil {
		return nil, false, err
	}
	return id, true, nil
}

func backupSetup(c hostConfig) error {
	id, made, err := backupKey()
	if err != nil {
		return err
	}

	fallback := c.Backups
	if fallback == "" {
		fallback = defaultBackups()
	}
	folder, err := input("Folder on this machine for backups", fallback)
	if err != nil {
		return err
	}
	if rest, ok := strings.CutPrefix(folder, "~/"); ok {
		home, _ := os.UserHomeDir()
		folder = filepath.Join(home, rest)
	}
	if folder, err = filepath.Abs(folder); err != nil {
		return err
	}

	answer, err := choose("Also send backups to an S3 bucket?", []choice{
		{label: "No", value: "no"},
		{label: "Yes", about: "AWS, Cloudflare R2, Backblaze B2, or another S3-compatible service", value: "yes"},
	}, "no")
	if err != nil {
		return err
	}
	var bucket []byte
	if answer == "yes" {
		if bucket, err = askBucket(); err != nil {
			return err
		}
	}

	mounted := running(c) && c.Backups == folder
	c.Backups = folder
	if err := os.MkdirAll(folder, 0o700); err != nil {
		return err
	}
	if err := c.save(); err != nil {
		return err
	}
	if !mounted {
		if err := launch(c, "Restarting den once what runs ends", recreate); err != nil {
			return err
		}
	}

	if err := insideQuiet(c, id.Recipient().String(), "secret", "set", backup.RecipientSecret); err != nil {
		return err
	}
	if bucket != nil {
		if err := insideQuiet(c, string(bucket), "secret", "set", backup.BucketSecret); err != nil {
			return err
		}
	} else {
		// A bucket set up before stays out.
		insideQuiet(c, "", "secret", "rm", backup.BucketSecret)
	}

	if err := step("Taking the first backup", func(context.Context) error { return insideQuiet(c, "", "backup") }); err != nil {
		return err
	}
	done("den backs up every %d hours and before each update, to %s%s", int(backup.Every.Hours()), folder, map[bool]string{true: " and S3"}[bucket != nil])
	if made {
		note("The key that opens the backups is " + backupKeyPath() + ". Keep a copy somewhere safe, such as a password manager: without it no backup can be read.")
	}
	return nil
}

// askBucket asks for an S3 bucket and returns it as the secret's JSON.
func askBucket() ([]byte, error) {
	var b backup.Bucket
	var err error
	if b.Endpoint, err = input("Endpoint", "https://s3.us-east-1.amazonaws.com"); err != nil {
		return nil, err
	}
	if b.Region, err = input("Region (auto for R2)", "us-east-1"); err != nil {
		return nil, err
	}
	if b.Name, err = ask("Bucket: ", false); err != nil {
		return nil, err
	}
	if b.Prefix, err = input("Prefix of the backups' names", "den/"); err != nil {
		return nil, err
	}
	if b.KeyID, err = ask("Access key ID: ", false); err != nil {
		return nil, err
	}
	if b.Secret, err = ask("Secret access key: ", true); err != nil {
		return nil, err
	}
	if err := b.Check(); err != nil {
		return nil, err
	}
	return json.Marshal(b)
}

// backupRestore replaces den's state with a backup: it opens the backup
// with the key on this machine, so den never holds the key, and unpacks
// it while den is stopped.
func backupRestore(c hostConfig, args []string) error {
	flags := flag.NewFlagSet("backup restore", flag.ContinueOnError)
	keyPath := flags.String("key", backupKeyPath(), "the file of the key that opens the backup")
	rest, err := parse(flags, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errors.New("backup restore takes the backup's file, or its name in the backups folder; den backup ls lists them")
	}

	path := rest[0]
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) && c.Backups != "" && filepath.Base(path) == path {
		path = filepath.Join(c.Backups, path)
	}
	plain, err := openBackup(path, *keyPath)
	if err != nil {
		return err
	}

	answer, err := choose("Replace den's state with "+filepath.Base(path)+"? What den holds now is lost", []choice{
		{label: "Cancel", value: "no"},
		{label: "Restore", value: "yes"},
	}, "no")
	if err != nil || answer != "yes" {
		return err
	}

	if c.exists("inspect", c.Name) {
		if err := step("Stopping den once what runs ends", func(context.Context) error { return c.quiet(stopArgs(c)...) }); err != nil {
			return err
		}
	}
	if !c.exists("volume", "inspect", c.Volume) {
		if err := c.quiet("volume", "create", c.Volume); err != nil {
			return err
		}
	}
	if !c.exists("image", "inspect", c.Image) {
		if err := pullOrBuild(c); err != nil {
			return err
		}
	}

	err = step("Restoring "+filepath.Base(path), func(context.Context) error {
		cmd := c.engine().command("run", "--rm", "-i", "-v", c.Volume+":/var/lib/den", c.Image, "den", "backup", "unpack")
		cmd.Stdin = bytes.NewReader(plain)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%v\n%s", err, lastLines(string(out), 20))
		}
		return nil
	})
	// A restore that failed left the state as it was, so den starts again
	// either way.
	if serr := launch(c, "Starting den", start); err == nil {
		err = serr
	}
	return err
}

// openBackup decrypts the backup at path whole, so a damaged one stops the
// restore before it changes anything.
func openBackup(path, keyPath string) ([]byte, error) {
	keys, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("the backups' key: %w", err)
	}
	ids, err := age.ParseIdentities(bytes.NewReader(keys))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", keyPath, err)
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r, err := age.Decrypt(f, ids...)
	if err != nil {
		return nil, fmt.Errorf("%s: the key does not open it: %w", path, err)
	}
	plain, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("%s is damaged: %w", path, err)
	}
	return plain, nil
}
