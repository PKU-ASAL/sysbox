package libvirt

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
)

// Config is the provider "libvirt" { ... } block decoded from HCL.
type Config struct {
	// VCPUs is the number of virtual CPUs. Defaults to 1.
	VCPUs int `hcl:"vcpus,optional"`
	// Memory is the RAM in MiB (e.g. "512", "2048"). Defaults to "512".
	Memory string `hcl:"memory,optional"`
	// MachineType is the QEMU machine type (e.g. "q35", "pc"). Defaults to "q35".
	MachineType string `hcl:"machine_type,optional"`
	// DiskSize is the logical disk size to grow the qcow2 image to (e.g.
	// "10G"). Empty means use the base image as-is.
	DiskSize string `hcl:"disk_size,optional"`
	// SSHUser is the username for SSH access. Defaults to "root".
	SSHUser string `hcl:"ssh_user,optional"`
	// SSHPass is the password for SSH access (used only when SSHKey is empty).
	SSHPass string `hcl:"ssh_pass,optional"`
	// SSHKey is the path to a private key for SSH access.
	SSHKey string `hcl:"ssh_key,optional"`
}

func (s *Substrate) DecodeProviderConfig(body hcl.Body, ctx *hcl.EvalContext) (any, error) {
	cfg := &Config{
		VCPUs:       1,
		Memory:      "512",
		MachineType: "q35",
		SSHUser:     "root",
	}
	if body == nil {
		return cfg, nil
	}
	if diag := gohcl.DecodeBody(body, ctx, cfg); diag.HasErrors() {
		return nil, fmt.Errorf("libvirt provider config: %s", diag.Error())
	}
	if cfg.VCPUs <= 0 {
		cfg.VCPUs = 1
	}
	if cfg.Memory == "" {
		cfg.Memory = "512"
	}
	if cfg.SSHUser == "" {
		cfg.SSHUser = "root"
	}
	return cfg, nil
}
