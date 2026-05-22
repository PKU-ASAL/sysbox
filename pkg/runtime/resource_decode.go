package runtime

import (
	"fmt"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/substrate"
)

func decodeDependsOn(deps []graph.Ref, items []string) []graph.Ref {
	for _, dep := range items {
		if parts := strings.SplitN(dep, ".", 2); len(parts) == 2 {
			deps = append(deps, graph.Ref{Type: parts[0], Name: parts[1]})
		}
	}
	return deps
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
