package firecracker

import (
	"context"
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/substrate"
)

// Config is the typed shape of the `provider "firecracker" {}` HCL block
// under a sysbox_node.
type Config struct {
	// Kernel is either a sysbox_kernel resource reference
	// ("sysbox_kernel.NAME.id" / "NAME") or a literal vmlinux path.
	// Runtime resolves references and rewrites this to an absolute path
	// before CreateNode.
	Kernel string `hcl:"kernel,optional"`

	// Rootfs overrides the image's rootfs; literal path to an ext4 file.
	Rootfs string `hcl:"rootfs,optional"`

	// ChainInit is the binary sysbox-init exec()s after setup; defaults to
	// /sbin/init inside the guest.
	ChainInit string `hcl:"chain_init,optional"`

	// SSH credentials used by the substrate's SSH fallback when the guest
	// rootfs lacks sysbox-init (vsock-rpc unavailable).
	SSHUser string `hcl:"ssh_user,optional"`
	SSHPass string `hcl:"ssh_pass,optional"`
	SSHPort int    `hcl:"ssh_port,optional"`
}

// DecodeProviderConfig decodes the provider block into *firecracker.Config.
func (s *Substrate) DecodeProviderConfig(body hcl.Body, ctx *hcl.EvalContext) (any, error) {
	cfg := &Config{}
	if body == nil {
		return cfg, nil
	}
	if diag := gohcl.DecodeBody(body, ctx, cfg); diag.HasErrors() {
		return nil, fmt.Errorf("firecracker: decode provider config: %s", diag.Error())
	}
	return cfg, nil
}

// PrepareHandle resolves kernel references and populates ConnInfo so runtime
// no longer needs to dispatch on concrete firecracker types.
//
// Steps:
//  1. If Config.Kernel is a sysbox_kernel ref, rewrite it to the local path.
//  2. If HandleState has a vsock UDS, the Conn is already set (ConnKindVsock);
//     nothing to do.
//  3. Otherwise (SSH fallback), populate HandleState.SSHIP/SSHPort and set
//     handle.Conn.Kind = ConnKindSSH.
func (s *Substrate) PrepareHandle(_ context.Context, handle *substrate.NodeHandle, pc any, st substrate.StateReader) error {
	cfg, _ := pc.(*Config)
	hs, _ := handle.Provider.(*HandleState)

	// Step 1: resolve kernel ref.
	if cfg != nil && cfg.Kernel != "" && config.LooksLikeKernelRef(cfg.Kernel) {
		kernelAddr, err := config.ResolveResourceAddress(cfg.Kernel, "sysbox_kernel")
		if err != nil {
			return err
		}
		inst := st.ResourceInstance(kernelAddr)
		if inst == nil {
			return fmt.Errorf("kernel %s not applied yet", kernelAddr)
		}
		path, _ := inst["path"].(string)
		if path == "" {
			return fmt.Errorf("kernel %s has no resolved path in state", kernelAddr)
		}
		cfg.Kernel = path
	}

	// Steps 2+3: populate ConnInfo.
	if hs == nil {
		return nil
	}
	// vsock already set by CreateNode — nothing to do.
	if hs.VsockUDS != "" {
		return nil
	}
	// SSH fallback: write coordinates into HandleState.
	hs.SSHIP = handle.Net.PrimaryIP
	port := 22
	if cfg != nil && cfg.SSHPort != 0 {
		port = cfg.SSHPort
	}
	hs.SSHPort = fmt.Sprintf("%d", port)
	handle.Conn.Kind = substrate.ConnKindSSH
	handle.Conn.Endpoint = fmt.Sprintf("%s:%s", hs.SSHIP, hs.SSHPort)
	return nil
}

// Dependencies reports the sysbox_kernel references the runtime must apply
// before the node is created.
func (s *Substrate) Dependencies(raw any) substrate.ProviderDeps {
	cfg, ok := raw.(*Config)
	if !ok || cfg == nil {
		return substrate.ProviderDeps{}
	}
	deps := substrate.ProviderDeps{}
	if cfg.Kernel != "" && config.LooksLikeKernelRef(cfg.Kernel) {
		if kernelAddr, err := config.ResolveResourceAddress(cfg.Kernel, "sysbox_kernel"); err == nil {
			deps.Kernels = append(deps.Kernels, kernelAddr.String())
		}
	}
	return deps
}
