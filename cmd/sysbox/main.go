package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/oslab/sysbox/cmd/sysbox/commands"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/driver"
	docker "github.com/oslab/sysbox/pkg/provider/docker"
	fc "github.com/oslab/sysbox/pkg/provider/firecracker"
	libvirt "github.com/oslab/sysbox/pkg/provider/libvirt"
	networkprovider "github.com/oslab/sysbox/pkg/provider/network"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	mustRegisterDriver(driver.Descriptor{Name: "network", Version: "1", LinuxNetwork: networkprovider.Driver{}})

	dockerSub, err := docker.New()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: docker substrate unavailable: %v\n", err)
	} else {
		mustRegisterDriver(driver.Descriptor{
			Name: "docker", Version: "1", Node: dockerSub, NIC: dockerSub,
			Console: dockerSub, GuestExec: dockerSub, Network: dockerSub,
			Artifact: dockerSub, Import: dockerSub,
			NodeState:     dockerSub,
			ImageEntry:    dockerSub,
			Power:         dockerSub,
			RouterNetwork: dockerSub,
			GuestNetwork:  dockerSub,
		})
	}

	cfg := config.MustLoadServiceConfig("")
	// Kernel/rootfs paths can be overridden per-node in HCL; these are defaults.
	kernelPath := cfg.Providers.Firecracker.Kernel
	if kernelPath == "" {
		kernelPath = "/tmp/vmlinux"
	}
	rootfsDir := cfg.Providers.Firecracker.Workdir
	fcSub := fc.New(kernelPath, rootfsDir)
	mustRegisterDriver(driver.Descriptor{
		Name: "firecracker", Version: "1", Node: fcSub, NIC: fcSub,
		Console: fcSub, GuestExec: fcSub, Artifact: fcSub,
		NodeState:    fcSub,
		Power:        fcSub,
		GuestNetwork: fcSub,
	})

	libvirtSub := libvirt.New()
	mustRegisterDriver(driver.Descriptor{
		Name: "libvirt", Version: "1", Node: libvirtSub, NIC: libvirtSub,
		Console: libvirtSub, Artifact: libvirtSub, Import: libvirtSub,
		NodeState: libvirtSub,
		Power:     libvirtSub,
	})

	if err := commands.ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}

func mustRegisterDriver(descriptor driver.Descriptor) {
	if err := driver.DefaultRegistry.Register(descriptor); err != nil {
		panic(err)
	}
}
