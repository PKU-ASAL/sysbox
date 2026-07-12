package runtime

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"

	"github.com/oslab/sysbox/pkg/address"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/substrate"
)

func decodeDependsOn(deps []address.Address, items []string) ([]address.Address, error) {
	for _, dep := range items {
		addr, err := address.Parse(dep)
		if err != nil {
			return nil, fmt.Errorf("invalid depends_on address %q: %w", dep, err)
		}
		deps = append(deps, addr)
	}
	return deps, nil
}

func decodeNodeProviderConfig(cfg *config.NodeConfig, ctx *hcl.EvalContext) error {
	subName, err := config.ResolveSubstrateRef(cfg.Substrate)
	if err != nil {
		return err
	}
	sub, err := substrate.Get(subName)
	if err != nil {
		return err
	}
	var body hcl.Body
	switch len(cfg.Providers) {
	case 0:
		body = nil
	case 1:
		pb := cfg.Providers[0]
		if pb.Type != subName {
			return fmt.Errorf("provider %q block does not match substrate %q", pb.Type, subName)
		}
		body = pb.Remain
	default:
		return fmt.Errorf("at most one provider block allowed per node, got %d", len(cfg.Providers))
	}
	pc, err := sub.DecodeProviderConfig(body, ctx)
	if err != nil {
		return err
	}
	cfg.ProviderConfig = pc
	return nil
}

func decodeDataBody(body hcl.Body, ctx *hcl.EvalContext, target any, typ, name string) error {
	if diag := gohcl.DecodeBody(body, ctx, target); diag.HasErrors() {
		return fmt.Errorf("data %s.%s: %s", typ, name, diag.Error())
	}
	return nil
}
