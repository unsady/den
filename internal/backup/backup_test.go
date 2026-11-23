package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/unsady/den/internal/claude"
	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/store"
)

func setUp(t *testing.T) (config.Config, *age.X25519Identity) {
	t.Helper()
	state := t.TempDir()
	cfg := config.Config{State: state, Secrets: filepath.Join(state, "secrets"), Backups: t.TempDir()}

	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(cfg.Secrets, RecipientSecret), id.Recipient().String()+"\n")
	write(t, filepath.Join(cfg.Secrets, "telegram_token"), "token\n")
	if err := os.Chmod(cfg.Secrets, 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(state, "jobs.json"), "[]")
	write(t, filepath.Join(state, "claude", "config", "login.json"), "{}")
	write(t, filepath.Join(state, "claude", claude.Version, "claude"), "binary")
	write(t, filepath.Join(state, "models", "ggml.bin"), "model")
	write(t, filepath.Join(state, "fx-settings.lock"), "")

	db, err := store.Open(filepath.Join(state, store.File))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return cfg, id
}

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSnapshotAndUnpack(t *testing.T) {
	cfg, id := setUp(t)
	ctx := context.Background()
	if !On(cfg) {
		t.Fatal("backups are off with a key and a folder")
	}

	name, err := Snapshot(ctx, cfg, time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if name != "den-20261123T090000Z.tar.gz.age" {
		t.Errorf("name %q", name)
	}

	data, err := os.ReadFile(filepath.Join(cfg.Backups, name))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("token")) {
		t.Fatal("the snapshot is not encrypted")
	}
	plain, err := age.Decrypt(bytes.NewReader(data), id)
	if err != nil {
		t.Fatal(err)
	}

	// After the snapshot, the state changes; a restore brings it back and
	// keeps what den downloads.
	write(t, filepath.Join(cfg.State, "jobs.json"), `[{"later": true}]`)
	write(t, filepath.Join(cfg.State, "runs", "new.log"), "later")
	write(t, filepath.Join(cfg.State, "models", "ggml.bin"), "newer model")
	if err := Unpack(cfg, plain); err != nil {
		t.Fatal(err)
	}

	if got := read(t, filepath.Join(cfg.State, "jobs.json")); got != "[]" {
		t.Errorf("jobs.json %q", got)
	}
	if got := read(t, filepath.Join(cfg.Secrets, "telegram_token")); got != "token\n" {
		t.Errorf("secret %q", got)
	}
	if got := read(t, filepath.Join(cfg.State, "claude", "config", "login.json")); got != "{}" {
		t.Errorf("claude login %q", got)
	}
	if got := read(t, filepath.Join(cfg.State, "models", "ggml.bin")); got != "newer model" {
		t.Errorf("the model was replaced: %q", got)
	}
	if got := read(t, filepath.Join(cfg.State, "claude", claude.Version, "claude")); got != "binary" {
		t.Errorf("claude's binary %q", got)
	}
	if _, err := os.Stat(filepath.Join(cfg.State, "runs", "new.log")); !os.IsNotExist(err) {
		t.Error("a file made after the snapshot stayed")
	}
	if fi, err := os.Stat(cfg.Secrets); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("secrets folder %v %v", fi.Mode(), err)
	}

	db, err := sql.Open("sqlite", filepath.Join(cfg.State, store.File))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow("SELECT count(*) FROM sessions").Scan(&n); err != nil {
		t.Fatalf("the restored database: %v", err)
	}
}

func TestUnpackLeavesTheStateOnABrokenSnapshot(t *testing.T) {
	cfg, _ := setUp(t)
	if err := Unpack(cfg, strings.NewReader("not a snapshot")); err == nil {
		t.Fatal("no error")
	}
	if got := read(t, filepath.Join(cfg.State, "jobs.json")); got != "[]" {
		t.Errorf("jobs.json %q", got)
	}
}

func TestExtractStaysInside(t *testing.T) {
	outside := t.TempDir()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: outside})
	tw.WriteHeader(&tar.Header{Name: "link/planted", Typeflag: tar.TypeReg, Mode: 0o600, Size: 1})
	tw.Write([]byte("x"))
	tw.Close()
	gz.Close()

	if err := extract(&buf, t.TempDir()); err == nil {
		t.Error("no error")
	}
	if _, err := os.Stat(filepath.Join(outside, "planted")); !os.IsNotExist(err) {
		t.Error("a file landed outside through a link")
	}
}

func TestSnapshotKeepsTheLatest(t *testing.T) {
	cfg, _ := setUp(t)
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for i := range Keep + 3 {
		if _, err := Snapshot(context.Background(), cfg, start.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}

	names, err := listLocal(cfg.Backups)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != Keep || names[0] != "den-20261101T030000Z.tar.gz.age" {
		t.Errorf("%d snapshots, first %q", len(names), names[0])
	}
}

func TestOff(t *testing.T) {
	cfg, _ := setUp(t)
	cfg.Backups = ""
	if On(cfg) {
		t.Error("on with nowhere to put a snapshot")
	}
	if _, err := Snapshot(context.Background(), cfg, time.Now()); err != ErrOff {
		t.Errorf("err %v", err)
	}
}

// AWS's example of a signed GET, from its Signature Version 4 docs for S3.
func TestSign(t *testing.T) {
	b := Bucket{Region: "us-east-1", KeyID: "AKIAIOSFODNN7EXAMPLE", Secret: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}
	req, _ := http.NewRequest(http.MethodGet, "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	req.Header.Set("Range", "bytes=0-9")
	b.sign(req, nil, time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC))

	want := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request," +
		"SignedHeaders=host;range;x-amz-content-sha256;x-amz-date," +
		"Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	if got := req.Header.Get("Authorization"); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// fakeS3 keeps objects of one bucket, lists them two to a page, and checks
// that each request is signed with the key.
type fakeS3 struct {
	t       *testing.T
	mu      sync.Mutex
	objects map[string][]byte
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.Contains(r.Header.Get("Authorization"), "Credential=key/") {
		http.Error(w, "unsigned", http.StatusForbidden)
		return
	}

	key, ok := strings.CutPrefix(r.URL.Path, "/bucket/")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/bucket":
		var keys []string
		for k := range f.objects {
			if strings.HasPrefix(k, r.URL.Query().Get("prefix")) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		start := 0
		if token := r.URL.Query().Get("continuation-token"); token != "" {
			fmt.Sscan(token, &start)
		}
		end := min(start+2, len(keys))
		var page struct {
			XMLName               xml.Name `xml:"ListBucketResult"`
			Contents              []struct{ Key string }
			IsTruncated           bool
			NextContinuationToken string `xml:",omitempty"`
		}
		for _, k := range keys[start:end] {
			page.Contents = append(page.Contents, struct{ Key string }{k})
		}
		if end < len(keys) {
			page.IsTruncated, page.NextContinuationToken = true, fmt.Sprint(end)
		}
		xml.NewEncoder(w).Encode(page)
	case ok && r.Method == http.MethodPut:
		data, _ := io.ReadAll(r.Body)
		f.objects[key] = data
	case ok && r.Method == http.MethodDelete:
		delete(f.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "unexpected", http.StatusBadRequest)
	}
}

func TestBucket(t *testing.T) {
	cfg, id := setUp(t)
	cfg.Backups = ""
	fake := &fakeS3{t: t, objects: map[string][]byte{"other/file": []byte("x")}}
	srv := httptest.NewTLSServer(fake)
	defer srv.Close()

	b := Bucket{Endpoint: srv.URL, Region: "auto", Bucket: "bucket", Prefix: "den/", KeyID: "key", Secret: "secret"}
	data, _ := json.Marshal(b)
	write(t, filepath.Join(cfg.Secrets, BucketSecret), string(data))
	// The fake's certificate is its own.
	defaultClient := http.DefaultTransport
	http.DefaultTransport = srv.Client().Transport
	defer func() { http.DefaultTransport = defaultClient }()

	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for i := range Keep + 2 {
		if _, err := Snapshot(context.Background(), cfg, start.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}

	_, remote, err := List(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(remote) != Keep || remote[0] != "den-20261101T020000Z.tar.gz.age" {
		t.Errorf("%d snapshots, first %q", len(remote), remote[0])
	}
	if _, ok := fake.objects["other/file"]; !ok {
		t.Error("an object outside the prefix was deleted")
	}

	last := fake.objects["den/"+remote[len(remote)-1]]
	if _, err := age.Decrypt(bytes.NewReader(last), id); err != nil {
		t.Errorf("the uploaded snapshot: %v", err)
	}
	if !slices.IsSorted(remote) {
		t.Error("not sorted")
	}
}

func TestCheck(t *testing.T) {
	good := Bucket{Endpoint: "https://s3.us-east-1.amazonaws.com", Region: "us-east-1", Bucket: "my-den", Prefix: "den/", KeyID: "k", Secret: "s"}
	if err := good.Check(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []func(*Bucket){
		func(b *Bucket) { b.Endpoint = "http://s3.example.com" },
		func(b *Bucket) { b.Endpoint = "https://s3.example.com/bucket" },
		func(b *Bucket) { b.Bucket = "My_Bucket" },
		func(b *Bucket) { b.Prefix = "/den" },
		func(b *Bucket) { b.Prefix = "den den" },
		func(b *Bucket) { b.Secret = "" },
	} {
		b := good
		bad(&b)
		if b.Check() == nil {
			t.Errorf("%+v passes", b)
		}
	}
}
