//go:build !windows

package windowstask

import (
	"os"
	"path/filepath"
)

func durableReplace(source, target string) error {
	if err := os.Rename(source, target); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(target))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
