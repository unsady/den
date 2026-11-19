package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file")

	// Writes at once each leave a whole file and no temporary one.
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			if err := Write(path, []byte(fmt.Sprint("value", i)), 0o644); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()

	data, _ := os.ReadFile(path)
	if len(data) != len("value0") {
		t.Fatalf("file %q", data)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("mode %v", info.Mode())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("left %v", entries)
	}

	// A failed write leaves nothing behind.
	if err := Write(filepath.Join(dir, "missing", "file"), nil, 0o600); err == nil {
		t.Fatal("wrote into a missing directory")
	}
}
