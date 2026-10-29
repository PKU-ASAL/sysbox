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
	if err := commands.ExecuteContextWithProviders(ctx, initializeProviders); err != nil {
		os.Exit(1)
	}
}

func initializeProviders(configPath string) error {
	// Provider constructors and preflight checks also read service defaults.
	// Pin the process to the explicit CLI config before any of them run.
	if configPath != "" {
		if err := os.Setenv("SYSBOX_CONFIG", configPath); err != nil {
			return err
		}
	}
	mustRegisterDriver(driver.Descriptor{Name: "network", Version: "1", LinuxNetwork: networkprovider.Driver{}, Policy: networkprovider.Driver{}})

	dockerSub, err := docker.New()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: docker substrate unavailable: %v\n", err)
	} else {
		if err := registerDockerDriver(driver.DefaultRegistry, dockerSub); err != nil {
			panic(err)
		}
	}

	cfg, err := config.LoadServiceConfig(configPath)
	if err != nil {
		return err
	}
	// Kernel/rootfs paths can be overridden per-node in HCL; these are defaults.
	kernelPath := cfg.Providers.Firecracker.Kernel
	if kernelPath == "" {
		kernelPath = "/tmp/vmlinux"
	}
	rootfsDir := cfg.Providers.Firecracker.Workdir
	fcSub := fc.New(kernelPath, rootfsDir)
	mustRegisterDriver(driver.Descriptor{
		Name: "firecracker", Version: "1", Node: fcSub, NIC: fcSub, Policy: fcSub,
		GuestExec: fcSub, GuestFiles: fcSub, Artifact: fcSub,
		NodeState:         fcSub,
		Power:             fcSub,
		GuestNetwork:      fcSub,
		OwnershipAdoption: fcSub,
		Reset:             fcSub,
	})

	libvirtSub := libvirt.New(cfg.Providers.Libvirt.Workdir)
	mustRegisterDriver(driver.Descriptor{
		Name: "libvirt", Version: "1", Node: libvirtSub, NIC: libvirtSub,
		GuestExec: libvirtSub, GuestFiles: libvirtSub,
		Artifact: libvirtSub, Import: libvirtSub,
		NodeState:         libvirtSub,
		Power:             libvirtSub,
		GuestNetwork:      libvirtSub,
		GuestNetworkInit:  libvirtSub,
		Reset:             libvirtSub,
		OwnershipAdoption: libvirtSub,
	})

	return nil
}

func mustRegisterDriver(descriptor driver.Descriptor) {
	if err := driver.DefaultRegistry.Register(descriptor); err != nil {
		panic(err)
	}
}

func registerDockerDriver(registry *driver.Registry, dockerSub *docker.Substrate) error {
	return registry.Register(driver.Descriptor{
		Name: "docker", Version: "1", Node: dockerSub, NIC: dockerSub,
		GuestExec: dockerSub, GuestFiles: dockerSub, Network: dockerSub,
		Artifact: dockerSub, Import: dockerSub,
		OwnershipAdoption: dockerSub,
		NodeState:         dockerSub,
		ImageEntry:        dockerSub,
		Power:             dockerSub,
		GuestNetwork:      dockerSub,
		Policy:            dockerSub,
		Reset:             dockerSub,
	})
}
