package network

import (
	"context"
	"fmt"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
	"os"
	"runtime"
)

// SetTapOwner grants the jailed VMM access to one already-owned TAP. The bridge
// and attachment identities remain locked/pinned throughout the ioctl.
func (b *OwnedBridge) SetTapOwner(ctx context.Context, proof AttachmentLink, alias string, uid uint32) (result error) {
	if err := b.ObserveAttachment(ctx, proof, alias); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- b.setTapOwner(ctx, proof, alias, uid) }()
	return <-done
}

func (b *OwnedBridge) setTapOwner(ctx context.Context, proof AttachmentLink, alias string, uid uint32) (result error) {
	runtime.LockOSThread()
	original, err := netns.Get()
	if err != nil {
		runtime.UnlockOSThread()
		return err
	}
	defer original.Close()
	if err = netns.Set(netns.NsHandle(b.ns.Fd())); err != nil {
		runtime.UnlockOSThread()
		return err
	}
	defer func() {
		if err := netns.Set(original); err != nil {
			result = fmt.Errorf("restore network namespace: %w", err)
		} else {
			runtime.UnlockOSThread()
		}
	}()
	// Unlock is handled by the restoring defer even if the operation fails.
	tun, err := os.OpenFile("/dev/net/tun", os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer tun.Close()
	req, err := unix.NewIfreq(proof.Name)
	if err != nil {
		return err
	}
	req.SetUint16(unix.IFF_TAP | unix.IFF_NO_PI)
	if err = unix.IoctlIfreq(int(tun.Fd()), unix.TUNSETIFF, req); err != nil {
		return err
	}
	if err = b.ObserveAttachment(ctx, proof, alias); err != nil {
		return err
	}
	return unix.IoctlSetInt(int(tun.Fd()), unix.TUNSETOWNER, int(uid))
}
