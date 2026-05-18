package firecracker

import (
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

// Dependencies reports the sysbox_kernel references the runtime must apply
// before the node is created.
func (s *Substrate) Dependencies(raw any) substrate.ProviderDeps {
	cfg, ok := raw.(*Config)
	if !ok || cfg == nil {
		return substrate.ProviderDeps{}
	}
	deps := substrate.ProviderDeps{}
	if cfg.Kernel != "" && config.LooksLikeKernelRef(cfg.Kernel) {
		if name := config.ResolveName(cfg.Kernel); name != "" {
			deps.Kernels = append(deps.Kernels, name)
		}
	}
	return deps
}
