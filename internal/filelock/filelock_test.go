package filelock

import (
	"path/filepath"
	"sync"
	"testing"
)

func TestLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")

	// Only one holder at a time.
	var wg sync.WaitGroup
	var mu sync.Mutex
	held := 0
	for range 8 {
		wg.Go(func() {
			unlock, err := Lock(path)
			if err != nil {
				t.Error(err)
				return
			}
			defer unlock()

			mu.Lock()
			held++
			n := held
			mu.Unlock()
			if n != 1 {
				t.Errorf("%d holders", n)
			}
			mu.Lock()
			held--
			mu.Unlock()
		})
	}
	wg.Wait()
}
