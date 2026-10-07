package hostsupervisor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// ValidateOwnedPath protects trust inputs and anchors against worker mutation.
// Ancestors may belong to root or the supervisor; the final object must belong
// to the supervisor effective UID. /tmp is the sole sticky-root exception.
func ValidateOwnedPath(path string, private bool) error {
	return validateOwnedPath(path, private, false)
}

func validateOwnedPath(path string, private, missing bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("trusted path must be absolute and clean")
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	current := "/"
	for i, part := range parts {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) && missing {
			return nil
		}
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("trusted path ownership unavailable")
		}
		last := i == len(parts)-1
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("trusted path contains symlink: %s", current)
		}
		if !last && !info.IsDir() {
			return fmt.Errorf("trusted path parent is not a directory")
		}
		if current == "/tmp" && !last && stat.Uid == 0 && info.IsDir() && info.Mode()&os.ModeSticky != 0 {
			continue
		}
		if (last && stat.Uid != uint32(os.Geteuid())) || (!last && stat.Uid != 0 && stat.Uid != uint32(os.Geteuid())) {
			return fmt.Errorf("trusted path owned by another user: %s", current)
		}
		if info.Mode().Perm()&0022 != 0 {
			return fmt.Errorf("trusted path writable by worker group or others: %s", current)
		}
		if last && private && info.Mode().Perm()&0077 != 0 {
			return fmt.Errorf("trusted path must be private: %s", current)
		}
		if last && !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("trusted path must be regular file or directory")
		}
	}
	return nil
}

func EnsureOwnedDirectory(path string, mode os.FileMode) error {
	if err := validateOwnedPath(path, mode.Perm()&0077 == 0, true); err != nil {
		return err
	}
	if err := os.MkdirAll(path, mode); err != nil {
		return err
	}
	if err := ValidateOwnedPath(path, mode.Perm()&0077 == 0); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("trusted path is not a directory")
	}
	return nil
}
