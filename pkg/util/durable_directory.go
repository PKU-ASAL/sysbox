package util

import (
	"fmt"
	"os"
	"path/filepath"
)

// MkdirAllDurable persists the complete directory chain, including each new
// entry in its parent. Syncing only the leaf does not protect a first-time
// inbox/checkpoint tree from power loss after acknowledgement.
func MkdirAllDurable(path string, mode os.FileMode) error {
	if err := os.MkdirAll(path, mode); err != nil {
		return err
	}
	for dir := filepath.Clean(path); ; dir = filepath.Dir(dir) {
		handle, err := os.Open(dir)
		if err != nil {
			return err
		}
		err = handle.Sync()
		closeErr := handle.Close()
		if err != nil {
			return fmt.Errorf("sync directory %s: %w", dir, err)
		}
		if closeErr != nil {
			return closeErr
		}
		if parent := filepath.Dir(dir); parent == dir {
			return nil
		}
	}
}
