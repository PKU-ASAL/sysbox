package guesttask

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os"
)

// Only guest-owned volatile tmpfs has a kernel-local lifetime proof. A block
// filesystem may itself sit on remote storage; its type cannot prove cessation.
func fileRebootSafeFilesystem(kind int64) bool { return kind == unix.TMPFS_MAGIC }
func validateFileRebootScope(parent *os.File, scope string) error {
	if scope == "" {
		return nil
	}
	if scope != "guest-tmpfs" {
		return fmt.Errorf("unsupported file reboot scope")
	}
	var info unix.Statfs_t
	if err := unix.Fstatfs(int(parent.Fd()), &info); err != nil {
		return err
	}
	if !fileRebootSafeFilesystem(int64(info.Type)) {
		return fmt.Errorf("reboot recovery requires guest tmpfs target")
	}
	return nil
}
