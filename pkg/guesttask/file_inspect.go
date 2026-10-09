package guesttask

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"

	"github.com/oslab/sysbox/pkg/substrate"
	"golang.org/x/sys/unix"
)

// InspectInstalledFile is historical-handle-authorized, read-only observation
// of a committed install's current target. It does not reconcile unknown intents.
func (e *Engine) InspectInstalledFile(ctx context.Context, h substrate.FileInstallHandle) (substrate.InstalledFileInspection, error) {
	unknown := substrate.InstalledFileInspection{Status: "unknown"}
	// Unknown/untrusted handles must not create store directories or lock files.
	prior, err := e.loadInstall(h.ID)
	if err != nil {
		return unknown, err
	}
	if !reflect.DeepEqual(prior.Handle, h) || prior.Phase != "committed" {
		return unknown, ErrConflict
	}
	store := e.installStore()
	unlock, err := store.lock(ctx, h.ID)
	if err != nil {
		return unknown, err
	}
	defer unlock()
	record, err := e.loadInstall(h.ID)
	if err != nil {
		return unknown, err
	}
	if !reflect.DeepEqual(record.Handle, h) || record.Phase != "committed" {
		return unknown, ErrConflict
	}
	boot, err := BootID()
	if err != nil {
		return unknown, err
	}
	if boot != h.BootID {
		return unknown, ErrConflict
	}
	parent, err := e.installParent(record.Request.Path)
	if err != nil {
		return unknown, err
	}
	defer parent.Close()
	parentID, err := installFDIdentity(parent)
	if err != nil {
		return unknown, err
	}
	if parentID != record.Parent {
		return unknown, ErrConflict
	}
	name := filepath.Base(record.Request.Path)
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err == unix.ENOENT {
		return substrate.InstalledFileInspection{Status: "absent"}, nil
	}
	if err != nil {
		return unknown, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	var before, after, current unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil {
		return unknown, err
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG || before.Size < 0 || before.Size > substrate.MaxFileInstallSize {
		return unknown, ErrConflict
	}
	if err := ctx.Err(); err != nil {
		return unknown, err
	}
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(file, substrate.MaxFileInstallSize+1))
	if err != nil {
		return unknown, err
	}
	if err := ctx.Err(); err != nil {
		return unknown, err
	}
	if err := unix.Fstat(fd, &after); err != nil {
		return unknown, err
	}
	if err := unix.Fstatat(int(parent.Fd()), name, &current, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return unknown, err
	}
	if before.Dev != after.Dev || before.Ino != after.Ino || before.Size != after.Size || before.Mode != after.Mode || before.Mtim != after.Mtim || before.Ctim != after.Ctim || current.Dev != after.Dev || current.Ino != after.Ino || current.Mode != after.Mode || current.Ctim != after.Ctim || size != after.Size {
		return unknown, ErrConflict
	}
	result := substrate.InstalledFileInspection{Status: "drifted", SHA256: fmt.Sprintf("%x", hash.Sum(nil)), Size: size, Mode: uint32(after.Mode & 07777)}
	if record.Anchor == (installIdentity{Device: uint64(after.Dev), Inode: after.Ino}) && result.SHA256 == record.Request.SHA256 && result.Size == record.Request.Size && result.Mode == record.Request.Mode {
		result.Status = "present"
	}
	return result, nil
}
