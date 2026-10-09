package libvirt

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/substrate"
)

// Config is the provider "libvirt" { ... } block decoded from HCL.
type Config struct {
	NetworkInit substrate.GuestNetworkInitMode `hcl:"network_init"`
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
	SSHKey           string `hcl:"ssh_key,optional"`
	SSHAuthorizedKey string `hcl:"ssh_authorized_key,optional"`
	// WindowsETW opt-in installs the standalone SysArmor ETW collector before
	// challenge provisioners. The PowerShell bootstrap requires a Windows guest.
	WindowsETW bool `hcl:"windows_etw,optional" json:"WindowsETW,omitempty"`
}

func (c *Config) BootstrapProvisioners() []config.ProvisionerConfig {
	if c == nil || !c.WindowsETW {
		return nil
	}
	const base = "/var/lib/sysbox/vm-artifacts/windows-etw/"
	const stage = `C:\ProgramData\SysArmor\Stage\`
	return []config.ProvisionerConfig{
		{Type: "exec", Program: `New-Item -ItemType Directory -Force -Path 'C:\ProgramData\SysArmor\Stage' | Out-Null`, Shell: "powershell"},
		{Type: "file", Source: base + "sysarmor-windows-etw.zip", Destination: stage + "sysarmor-windows-etw.zip"},
		{Type: "file", Source: base + "install-windows-etw.ps1", Destination: stage + "install-windows-etw.ps1"},
		{Type: "exec", Program: `& 'C:\ProgramData\SysArmor\Stage\install-windows-etw.ps1' -Archive 'C:\ProgramData\SysArmor\Stage\sysarmor-windows-etw.zip'`, Shell: "powershell"},
	}
}

func (c *Config) ObservationSource() string {
	if c != nil && c.WindowsETW {
		return "etw"
	}
	return ""
}

func (s *Substrate) DecodeProviderConfig(body hcl.Body, ctx *hcl.EvalContext) (any, error) {
	cfg := &Config{
		VCPUs:       1,
		Memory:      "512",
		MachineType: "q35",
		SSHUser:     "root",
	}
	if body == nil {
		return nil, fmt.Errorf("libvirt provider config: network_init is required")
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
	if !supportedNetworkInitMode(cfg.NetworkInit) {
		return nil, fmt.Errorf("libvirt provider config: network_init must be %q or %q, got %q", substrate.GuestNetworkInitCloudInit, substrate.GuestNetworkInitPreconfigured, cfg.NetworkInit)
	}
	return cfg, nil
}

func supportedNetworkInitMode(mode substrate.GuestNetworkInitMode) bool {
	return mode == substrate.GuestNetworkInitCloudInit || mode == substrate.GuestNetworkInitPreconfigured
}
