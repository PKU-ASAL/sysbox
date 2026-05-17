package docker

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"

	"github.com/oslab/sysbox/pkg/substrate"
)

// Config is the typed shape of the `provider "docker" {}` HCL block under a
// sysbox_node. All fields are optional; defaults match Docker's defaults.
type Config struct {
	// Privileged grants the container CAP_SYS_ADMIN + access to all devices;
	// required for eBPF/tracee inside the container.
	Privileged bool `hcl:"privileged,optional"`

	// PidMode = "host" shares the host PID namespace (docker run --pid=host).
	PidMode string `hcl:"pid_mode,optional"`

	// CgroupnsMode = "host" shares the host cgroup namespace.
	CgroupnsMode string `hcl:"cgroupns_mode,optional"`

	// Binds is the list of "host:container[:options]" bind mounts.
	Binds []string `hcl:"binds,optional"`
}

// DecodeProviderConfig decodes the provider block into *docker.Config.
// A nil body yields a zero-value Config (all defaults).
func (s *Substrate) DecodeProviderConfig(body hcl.Body, ctx *hcl.EvalContext) (any, error) {
	cfg := &Config{}
	if body == nil {
		return cfg, nil
	}
	if diag := gohcl.DecodeBody(body, ctx, cfg); diag.HasErrors() {
		return nil, fmt.Errorf("docker: decode provider config: %s", diag.Error())
	}
	return cfg, nil
}

// Dependencies returns an empty ProviderDeps; the docker provider block holds
// only inline values, no cross-resource references.
func (s *Substrate) Dependencies(any) substrate.ProviderDeps {
	return substrate.ProviderDeps{}
}
