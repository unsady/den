// Package backup snapshots den's state: its database and the files in its
// state folder, such as the secrets and the jobs. A snapshot is encrypted
// to a key whose private half stays with the user, since it goes where the
// agent can read it, such as a folder of the host mounted in the
// container, or to an S3 bucket.
//
// The agent's home, with its space and fx's sessions, is the host's own
// folder and is not in a snapshot.
package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"filippo.io/age"

	"github.com/unsady/den/internal/atomicfile"
	"github.com/unsady/den/internal/claude"
	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/filelock"
	"github.com/unsady/den/internal/store"
)

const (
	// RecipientSecret holds the age public key snapshots are encrypted to.
	// It is a secret only so the agent cannot replace it with its own.
	RecipientSecret = "backup_recipient"
	// Keep is how many snapshots each place keeps.
	Keep = 14
	// Every is how often den takes a snapshot.
	Every = 6 * time.Hour

	prefix = "den-"
	suffix = ".tar.gz.age"
	// temp starts the names of the folders a snapshot or a restore works
	// in, inside the state.
	temp = ".backup-"
)

// ErrOff means backups are not set up: there is no key, or nowhere to put
// a snapshot.
var ErrOff = errors.New("backups are not set up: run den backup setup on the host")

// On reports whether backups are set up. A bucket that den cannot read
// counts, so its error reaches the user.
func On(cfg config.Config) bool {
	if _, err := os.Stat(filepath.Join(cfg.Secrets, RecipientSecret)); err != nil {
		return false
	}
	_, err := os.Stat(filepath.Join(cfg.Secrets, BucketSecret))
	return cfg.Backups != "" || err == nil
}

// Snapshot takes a snapshot and saves it to the folder and the bucket,
// keeping the latest Keep in each. It returns the snapshot's name, and
// with an error from one place still saves to the other.
func Snapshot(ctx context.Context, cfg config.Config, now time.Time) (string, error) {
	unlock, err := filelock.Lock(filepath.Join(cfg.State, "backup.lock"))
	if err != nil {
		return "", err
	}
	defer unlock()
	// A snapshot that fails waits for the next one too, so den does not
	// retry it all day.
	if err := tried(cfg, now); err != nil {
		return "", err
	}

	recipient, err := readRecipient(cfg)
	if err != nil {
		return "", err
	}
	var errs []error
	bucket, err := loadBucket(cfg)
	if err != nil {
		errs = append(errs, fmt.Errorf("S3: %w", err))
	}
	if cfg.Backups == "" && bucket == nil {
		if len(errs) > 0 {
			return "", errors.Join(errs...)
		}
		return "", ErrOff
	}

	data, err := pack(ctx, cfg, recipient)
	if err != nil {
		return "", err
	}

	name := snapshotName(now)
	if cfg.Backups != "" {
		if err := saveLocal(cfg.Backups, name, data); err != nil {
			errs = append(errs, fmt.Errorf("folder: %w", err))
		}
	}
	if bucket != nil {
		if err := bucket.save(ctx, name, data); err != nil {
			errs = append(errs, fmt.Errorf("S3: %w", err))
		}
	}
	return name, errors.Join(errs...)
}

func snapshotName(t time.Time) string {
	return prefix + t.UTC().Format("20060102T150405Z") + suffix
}

// List returns the snapshots in the folder and in the bucket, oldest first.
// A place that fails to list leaves the other's list as it is.
func List(ctx context.Context, cfg config.Config) (local, remote []string, err error) {
	var errs []error
	if cfg.Backups != "" {
		if local, err = listLocal(cfg.Backups); err != nil {
			errs = append(errs, fmt.Errorf("folder: %w", err))
		}
	}

	bucket, err := loadBucket(cfg)
	if err == nil && bucket != nil {
		remote, err = bucket.snapshots(ctx)
	}
	if err != nil {
		errs = append(errs, fmt.Errorf("S3: %w", err))
	}
	return local, remote, errors.Join(errs...)
}

// Unpack replaces den's state with the snapshot tar.gz in r, decrypted.
// den must not run. The state changes only once the whole snapshot is
// read, and goes back as it was if the swap fails; what den downloads
// again stays.
func Unpack(cfg config.Config, r io.Reader) error {
	if _, err := os.Stat(cfg.Socket); err == nil {
		return errors.New("den runs: den backup restore on the host stops it first")
	}

	staging, err := os.MkdirTemp(cfg.State, temp+"restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)

	if err := extract(r, staging); err != nil {
		return fmt.Errorf("the snapshot is not whole: %w", err)
	}
	if _, err := os.Stat(filepath.Join(staging, store.File)); err != nil {
		return errors.New("the snapshot has no database")
	}

	old, err := os.MkdirTemp(cfg.State, temp+"old-")
	if err != nil {
		return err
	}
	if err := aside(cfg.State, "", old); err != nil {
		return putBack(cfg.State, old, err)
	}
	if err := merge(staging, cfg.State); err != nil {
		return putBack(cfg.State, old, err)
	}
	return os.RemoveAll(old)
}

// putBack brings the state set aside in old back after a restore failed
// with cause.
func putBack(state, old string, cause error) error {
	failed, err := os.MkdirTemp(state, temp+"failed-")
	if err == nil {
		err = aside(state, "", failed)
	}
	if err == nil {
		err = merge(old, state)
	}
	if err != nil {
		return fmt.Errorf("%w; the state before the restore is in %s, as putting it back failed: %v", cause, old, err)
	}
	os.RemoveAll(failed)
	os.RemoveAll(old)
	return cause
}

// downloaded are the state's paths den downloads again.
var downloaded = []string{"models", "lost+found", filepath.Join("claude", claude.Version), filepath.Join("claude", ".cache")}

// kept are the state's paths a restore leaves as they are and a snapshot
// leaves out: what den downloads again, and the folders backups work in.
func kept(rel string) bool {
	return slices.Contains(downloaded, rel) || strings.HasPrefix(rel, temp)
}

// holdsKept reports whether the folder at rel holds a path that kept
// names.
func holdsKept(rel string) bool {
	return slices.ContainsFunc(downloaded, func(p string) bool {
		return strings.HasPrefix(p, rel+string(filepath.Separator))
	})
}

// packed reports whether a snapshot takes the state's file at rel as it
// is. The database goes in as a copy, and locks only matter to the
// processes that hold them.
func packed(rel string) bool {
	switch rel {
	case store.File, store.File + "-wal", store.File + "-shm":
		return false
	}
	return !kept(rel) && !strings.HasSuffix(rel, ".lock")
}

func readRecipient(cfg config.Config) (age.Recipient, error) {
	data, err := os.ReadFile(filepath.Join(cfg.Secrets, RecipientSecret))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrOff
	}
	if err != nil {
		return nil, err
	}
	return age.ParseX25519Recipient(strings.TrimSpace(string(data)))
}

// pack writes the state as an encrypted tar.gz.
func pack(ctx context.Context, cfg config.Config, recipient age.Recipient) ([]byte, error) {
	dir, err := os.MkdirTemp(cfg.State, temp)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	db := filepath.Join(dir, store.File)
	if err := store.Copy(ctx, filepath.Join(cfg.State, store.File), db); err != nil {
		return nil, fmt.Errorf("copy the database: %w", err)
	}
	// The copy is restored as it is, and the database is den's alone.
	if err := os.Chmod(db, 0o600); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	enc, err := age.Encrypt(&buf, recipient)
	if err != nil {
		return nil, err
	}
	gz := gzip.NewWriter(enc)
	tw := tar.NewWriter(gz)

	if err := addFile(tw, db, store.File); err != nil {
		return nil, err
	}
	err = filepath.WalkDir(cfg.State, func(path string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(cfg.State, path)
		if err != nil || rel == "." {
			return err
		}
		if !packed(rel) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		return addFile(tw, path, rel)
	})
	if err != nil {
		return nil, err
	}

	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// addFile adds the folder, file, or link at path to the archive as rel.
// A file that went away in the meantime is left out, as is anything else,
// such as a socket.
func addFile(tw *tar.Writer, path, rel string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}

	link := ""
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		if link, err = os.Readlink(path); err != nil {
			return err
		}
	case !info.IsDir() && !info.Mode().IsRegular():
		return nil
	}

	hdr, err := tar.FileInfoHeader(info, link)
	if err != nil {
		return err
	}
	hdr.Name = filepath.ToSlash(rel)
	if info.IsDir() {
		hdr.Name += "/"
	}
	if !info.Mode().IsRegular() {
		return tw.WriteHeader(hdr)
	}

	// The file is read whole first, so one den changes meanwhile goes in
	// at the size it has when read.
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	hdr.Size = int64(len(data))

	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err = tw.Write(data)
	return err
}

// extract unpacks the tar.gz in r into dir, with the owners and modes it
// records. Nothing it writes lands outside dir, even through a link the
// archive made.
func extract(r io.Reader, dir string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()

	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return gz.Close()
		}
		if err != nil {
			return err
		}

		name := filepath.FromSlash(strings.TrimSuffix(hdr.Name, "/"))
		if !filepath.IsLocal(name) {
			return fmt.Errorf("%q is outside the state", hdr.Name)
		}
		if parent := filepath.Dir(name); parent != "." {
			if err := root.MkdirAll(parent, 0o755); err != nil {
				return err
			}
		}

		mode := fs.FileMode(hdr.Mode).Perm()
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(name, mode); err != nil {
				return err
			}
			if err := root.Chmod(name, mode); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := root.Symlink(hdr.Linkname, name); err != nil {
				return err
			}
		case tar.TypeReg:
			f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, tr)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		default:
			continue
		}
		if os.Geteuid() == 0 {
			if err := root.Lchown(name, hdr.Uid, hdr.Gid); err != nil {
				return err
			}
		}
	}
}

// aside moves what is in the folder dir, at rel in the state, into to,
// except what kept names.
func aside(dir, rel, to string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}

	for _, e := range entries {
		r := filepath.Join(rel, e.Name())
		if kept(r) {
			continue
		}

		from, dst := filepath.Join(dir, e.Name()), filepath.Join(to, e.Name())
		if !e.IsDir() || !holdsKept(r) {
			if err := os.Rename(from, dst); err != nil {
				return err
			}
			continue
		}

		// A folder that holds what stays, such as claude's, stays too.
		info, err := e.Info()
		if err != nil {
			return err
		}
		if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
			return err
		}
		if err := aside(from, r, dst); err != nil {
			return err
		}
	}
	return nil
}

// merge moves what is in the folder src into dst, which is on the same
// disk.
func merge(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, e := range entries {
		from, to := filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())
		if !e.IsDir() {
			if err := os.Rename(from, to); err != nil {
				return err
			}
			continue
		}

		if _, err := os.Stat(to); errors.Is(err, fs.ErrNotExist) {
			if err := os.Rename(from, to); err != nil {
				return err
			}
			continue
		}
		if err := merge(from, to); err != nil {
			return err
		}
	}
	return nil
}

func saveLocal(dir, name string, data []byte) error {
	path := filepath.Join(dir, name)
	if err := atomicfile.Write(path, data, 0o600); err != nil {
		return err
	}
	if err := ownLike(path, dir); err != nil {
		return err
	}

	names, err := listLocal(dir)
	if err != nil {
		return err
	}
	for _, old := range stale(names, name) {
		if err := os.Remove(filepath.Join(dir, old)); err != nil {
			return err
		}
	}
	return nil
}

func listLocal(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var names []string
	for _, e := range entries {
		if isSnapshot(e.Name()) && e.Type().IsRegular() {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)
	return names, nil
}

func isSnapshot(name string) bool {
	return strings.HasPrefix(name, prefix) && strings.HasSuffix(name, suffix)
}

// ownLike gives the file at path the owner of the folder dir: den runs as
// root, and the user on the host reads the folder's snapshots to restore
// one.
func ownLike(path, dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) == os.Geteuid() {
		return nil
	}
	return os.Chown(path, int(st.Uid), int(st.Gid))
}

// stale returns the snapshots, sorted oldest first, beyond the latest Keep
// up to latest, the one just saved. Later names, as from a clock that ran
// ahead or a file someone put there, neither count nor go.
func stale(names []string, latest string) []string {
	n, _ := slices.BinarySearch(names, latest)
	if n < len(names) && names[n] == latest {
		n++
	}
	if n <= Keep {
		return nil
	}
	return names[:n-Keep]
}

// Last returns when den last tried to take a snapshot.
func Last(cfg config.Config) time.Time {
	var v struct{ Last time.Time }
	data, _ := os.ReadFile(lastPath(cfg))
	json.Unmarshal(data, &v)
	return v.Last
}

// tried records that den tried to take a snapshot at t.
func tried(cfg config.Config, t time.Time) error {
	data, err := json.Marshal(struct{ Last time.Time }{t})
	if err != nil {
		return err
	}
	return atomicfile.Write(lastPath(cfg), data, 0o600)
}

func lastPath(cfg config.Config) string { return filepath.Join(cfg.State, "backup.json") }
