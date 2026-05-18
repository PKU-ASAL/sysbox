package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/oslab/sysbox/cmd/sysbox/commands"
	docker "github.com/oslab/sysbox/pkg/provider/docker"
	fc "github.com/oslab/sysbox/pkg/provider/firecracker"
	_ "github.com/oslab/sysbox/pkg/provider/libvirt" // registers "libvirt" substrate via init()
	"github.com/oslab/sysbox/pkg/substrate"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	dockerSub, err := docker.New()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: docker substrate unavailable: %v\n", err)
	} else {
		substrate.Register(dockerSub)
	}

	// Register firecracker substrate if binary and kernel are available.
	// Kernel/rootfs paths can be overridden per-node in HCL; these are defaults.
	kernelPath := os.Getenv("SYSBOX_FC_KERNEL")
	if kernelPath == "" {
		kernelPath = "/tmp/vmlinux"
	}
	rootfsDir := os.Getenv("SYSBOX_FC_ROOTFS_DIR")
	if rootfsDir == "" {
		rootfsDir = "/tmp/fc-images"
	}
	fcSub := fc.New(kernelPath, rootfsDir)
	substrate.Register(fcSub)

	if err := commands.ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}
