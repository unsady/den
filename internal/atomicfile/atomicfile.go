// Package atomicfile replaces a file whole, so a reader sees either the old
// file or the new one, never a part of either.
package atomicfile

import (
	"os"
	"path/filepath"
)

// Write writes data to a new file next to path and renames it over path.
// Each write has its own temporary file, ending in .tmp, so two writes at
// once never mix.
func Write(path string, data []byte, perm os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()

	_, err = f.Write(data)
	if err == nil {
		err = f.Chmod(perm)
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}
