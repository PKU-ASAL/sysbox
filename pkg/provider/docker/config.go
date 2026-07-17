package docker

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/zclconf/go-cty/cty"

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

	Entrypoint OptionalArgv
	Command    OptionalArgv
}

// OptionalArgv distinguishes an omitted launch field from an explicit empty
// array, which clears the corresponding value inherited from the OCI image.
type OptionalArgv struct {
	Set   bool     `json:"set"`
	Value []string `json:"value,omitempty"`
}

type rawConfig struct {
	Privileged   bool           `hcl:"privileged,optional"`
	PidMode      string         `hcl:"pid_mode,optional"`
	CgroupnsMode string         `hcl:"cgroupns_mode,optional"`
	Binds        []string       `hcl:"binds,optional"`
	Entrypoint   hcl.Expression `hcl:"entrypoint,optional"`
	Command      hcl.Expression `hcl:"command,optional"`
}

// DecodeProviderConfig decodes the provider block into *docker.Config.
// A nil body yields a zero-value Config (all defaults).
func (s *Substrate) DecodeProviderConfig(body hcl.Body, ctx *hcl.EvalContext) (any, error) {
	cfg := &Config{}
	if body == nil {
		return cfg, nil
	}
	raw := &rawConfig{}
	if diag := gohcl.DecodeBody(body, ctx, raw); diag.HasErrors() {
		return nil, fmt.Errorf("docker: decode provider config: %s", diag.Error())
	}
	attributes, diagnostics := body.JustAttributes()
	if diagnostics.HasErrors() {
		return nil, fmt.Errorf("docker: decode provider config: %s", diagnostics.Error())
	}
	cfg.Privileged = raw.Privileged
	cfg.PidMode = raw.PidMode
	cfg.CgroupnsMode = raw.CgroupnsMode
	cfg.Binds = raw.Binds
	var err error
	if attribute, present := attributes["entrypoint"]; present {
		if cfg.Entrypoint, err = decodeArgv("entrypoint", attribute.Expr, ctx); err != nil {
			return nil, err
		}
	}
	if attribute, present := attributes["command"]; present {
		if cfg.Command, err = decodeArgv("command", attribute.Expr, ctx); err != nil {
			return nil, err
		}
	}
	return cfg, nil
}

func decodeArgv(name string, expression hcl.Expression, ctx *hcl.EvalContext) (OptionalArgv, error) {
	value, diagnostics := expression.Value(ctx)
	if diagnostics.HasErrors() {
		return OptionalArgv{}, fmt.Errorf("docker: decode %s: %s", name, diagnostics.Error())
	}
	valueType := value.Type()
	if value.IsNull() || !value.IsKnown() || !value.CanIterateElements() || (!valueType.Equals(cty.EmptyTuple) && !valueType.IsTupleType() && !valueType.IsListType()) {
		return OptionalArgv{}, fmt.Errorf("docker: %s must be an array of strings", name)
	}
	result := OptionalArgv{Set: true, Value: []string{}}
	iterator := value.ElementIterator()
	for iterator.Next() {
		_, element := iterator.Element()
		if element.IsNull() || !element.IsKnown() || element.Type() != cty.String {
			return OptionalArgv{}, fmt.Errorf("docker: %s must be an array of strings", name)
		}
		result.Value = append(result.Value, element.AsString())
	}
	return result, nil
}

func effectiveLaunch(imageEntrypoint, imageCommand []string, cfg *Config) ([]string, []string) {
	entrypoint := append([]string(nil), imageEntrypoint...)
	command := append([]string(nil), imageCommand...)
	if cfg != nil && cfg.Entrypoint.Set {
		entrypoint = append([]string{}, cfg.Entrypoint.Value...)
	}
	if cfg != nil && cfg.Command.Set {
		command = append([]string{}, cfg.Command.Value...)
	}
	return entrypoint, command
}

// Dependencies returns an empty ProviderDeps; the docker provider block holds
// only inline values, no cross-resource references.
func (s *Substrate) Dependencies(any) substrate.ProviderDeps {
	return substrate.ProviderDeps{}
}
