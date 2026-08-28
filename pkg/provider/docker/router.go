package docker

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"

	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
)

func (s *Substrate) resolveAttachmentDevice(ctx context.Context, handle substrate.NodeHandle, req driver.AttachmentRequest, result driver.AttachmentResult) (string, error) {
	if result.GuestDevice != "" {
		return result.GuestDevice, nil
	}
	if len(req.IPPrefixes) == 0 {
		return "", fmt.Errorf("attachment %q has no observed device or IP", req.Name)
	}
	want := strings.SplitN(req.IPPrefixes[0], "/", 2)[0]
	// Docker assigns the endpoint IP asynchronously after network connect, so
	// poll briefly for the interface to appear. TODO(nat): once end-to-end NAT
	// is confirmed working, replace this busy-wait with a netlink address
	// subscription so the NAT hot path never sleeps.
	delay := 10 * time.Millisecond
	for attempt := 0; attempt < 8; attempt++ {
		device, err := s.deviceNameForIP(ctx, handle, want)
		if err == nil && device != "" {
			return device, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(delay):
		}
		if delay < 100*time.Millisecond {
			delay *= 2
		}
	}
	return "", fmt.Errorf("resolve attachment %q: no interface has IP %s", req.Name, want)
}

// deviceNameForIP resolves the guest interface carrying the given IP by
// entering the container's network namespace from the host and listing IPv4
// addresses via netlink. This avoids depending on `ip` being installed in the
// guest image (e.g. ubuntu:24.04 lacks iproute2).
func (s *Substrate) deviceNameForIP(ctx context.Context, handle substrate.NodeHandle, want string) (string, error) {
	ins, err := s.cli.ContainerInspect(ctx, handle.ID)
	if err != nil {
		return "", err
	}
	if ins.State == nil || ins.State.Pid == 0 {
		return "", nil
	}
	return netnsDeviceForIP(ins.State.Pid, want)
}

func netnsDeviceForIP(pid int, want string) (string, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	orig, err := netns.Get()
	if err != nil {
		return "", err
	}
	defer orig.Close()

	ns, err := netns.GetFromPid(pid)
	if err != nil {
		return "", err
	}
	defer ns.Close()

	if err := netns.Set(ns); err != nil {
		return "", err
	}
	defer func() { _ = netns.Set(orig) }()

	addrs, err := netlink.AddrList(nil, netlink.FAMILY_V4)
	if err != nil {
		return "", err
	}
	for _, addr := range addrs {
		if addr.IP != nil && addr.IP.String() == want {
			link, err := netlink.LinkByIndex(addr.LinkIndex)
			if err != nil {
				return "", err
			}
			return link.Attrs().Name, nil
		}
	}
	return "", nil
}
