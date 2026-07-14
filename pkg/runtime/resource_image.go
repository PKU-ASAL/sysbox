package runtime

import (
	"context"
	"fmt"

	"github.com/hashicorp/hcl/v2"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/secret"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

type ImageResourceHandler struct{}

func init() {
	RegisterResourceHandler(ImageResourceHandler{})
}

func (ImageResourceHandler) Type() string { return "sysbox_image" }

func (ImageResourceHandler) Schema() ResourceSchema {
	return ResourceSchemaFor("sysbox_image")
}

func (ImageResourceHandler) Read(_ context.Context, current state.Resource) (ResourceReadResult, error) {
	result := resourceReadOK(current)
	result.Reason = "resource has no runtime health probe"
	return result, nil
}

func (ImageResourceHandler) PlanDiff(desired *graph.Node, current *state.Resource) (controlplane.PlannedChange, error) {
	return planDiffByDesiredHash(desired, current)
}

func (ImageResourceHandler) Create(ctx context.Context, pc *ProviderContext, n *graph.Node) (state.Resource, error) {
	cfg, ok := n.Data.(*config.ImageConfig)
	if !ok {
		return state.Resource{}, fmt.Errorf("image %s: wrong data type", n.Address)
	}
	subName, err := resolveSubstrateRef(cfg.Substrate)
	if err != nil {
		return state.Resource{}, err
	}
	artifactDriver, err := driver.DefaultRegistry.RequireArtifact(subName)
	if err != nil {
		return state.Resource{}, err
	}

	resolvedSource, err := secret.ResolveString(ctx, executionSecretResolver, cfg.Source)
	if err != nil {
		return state.Resource{}, fmt.Errorf("image %s source: %w", n.Address.Name, err)
	}
	handle, err := artifactDriver.ResolveImage(ctx, substrate.ArtifactSource{
		Kind: substrate.ArtifactKind(cfg.Kind), Source: cfg.Source, ResolvedSource: resolvedSource,
		ExpectedDigest: cfg.SHA256, Architecture: cfg.Architecture,
		GuestFamily: substrate.GuestFamily(cfg.GuestFamily), Size: cfg.Size,
	})
	if err != nil {
		return state.Resource{}, err
	}
	if err := handle.Validate(); err != nil {
		return state.Resource{}, fmt.Errorf("image %s: invalid resolved identity: %w", n.Address.Name, err)
	}

	inst := map[string]any{
		"image_id":     handle.ID,
		"repository":   handle.Identity.Source,
		"kind":         string(handle.Identity.Kind),
		"source":       handle.Identity.Source,
		"sha256":       handle.Identity.Digest,
		"architecture": handle.Identity.Architecture,
		"guest_family": string(handle.Identity.GuestFamily),
		"metadata":     handle.Identity.Metadata,
	}
	if err := setDesiredHash(n, inst); err != nil {
		return state.Resource{}, err
	}
	return state.Resource{
		Address:    n.Address,
		Driver:     subName,
		Attributes: state.MustAttributes(inst),
	}, nil
}

func (ImageResourceHandler) Delete(_ context.Context, pc *ProviderContext, current state.Resource) error {
	pc.State().RemoveResource(current.Address)
	return nil
}

func (ImageResourceHandler) ExternalID(current state.Resource) string {
	if id := current.ImageID(); id != "" {
		return id
	}
	return current.Str("id")
}

func artifactHandleFromState(resource *state.Resource) substrate.ArtifactHandle {
	if resource == nil {
		return substrate.ArtifactHandle{}
	}
	return substrate.ArtifactHandle{ID: resource.ImageID(), Identity: substrate.ArtifactIdentity{
		Kind: substrate.ArtifactKind(resource.Str("kind")), Source: resource.Str("source"), Digest: resource.Str("sha256"),
		Architecture: resource.Str("architecture"), GuestFamily: substrate.GuestFamily(resource.Str("guest_family")),
	}}
}
func (ImageResourceHandler) RequiredCapabilities(node *graph.Node) ([]CapabilityRequirement, error) {
	cfg, ok := node.Data.(*config.ImageConfig)
	if !ok {
		return nil, nil
	}
	name, err := resolveSubstrateRef(cfg.Substrate)
	if err != nil {
		return nil, err
	}
	return []CapabilityRequirement{{name, driver.CapabilityArtifact}}, nil
}

func (ImageResourceHandler) DecodeResource(r config.ResourceBlock, _ string, ctx *hcl.EvalContext) (any, []address.Address, error) {
	cfg := &config.ImageConfig{}
	if err := config.DecodeResource(&r, cfg, ctx); err != nil {
		return nil, nil, err
	}
	return cfg, nil, nil
}

func (ImageResourceHandler) PreflightResource(r config.ResourceBlock, ctx *hcl.EvalContext) []substrate.PreflightCheck {
	cfg := &config.ImageConfig{}
	if err := config.DecodeResource(&r, cfg, ctx); err != nil {
		return []substrate.PreflightCheck{DecodePreflightError(r.Type, r.Name, err)}
	}
	var checks []substrate.PreflightCheck
	if substrate.ArtifactKind(cfg.Kind) != substrate.ArtifactOCI {
		if check := ArtifactPreflightCheck("image:"+r.Name+":"+cfg.Kind, cfg.Source, cfg.SHA256); check != nil {
			checks = append(checks, *check)
		}
	}
	return checks
}

func (DataImageResourceHandler) DecodeData(d config.DataBlock, ctx *hcl.EvalContext) (any, []address.Address, error) {
	cfg := &config.DataImageConfig{}
	if err := decodeDataBody(d.Remain, ctx, cfg, "sysbox_image", d.Name); err != nil {
		return nil, nil, err
	}
	var deps []address.Address
	if ref := config.ResolveName(cfg.Substrate); ref != "" {
		deps = append(deps, address.Address{Type: "substrate", Name: ref})
	}
	return cfg, deps, nil
}
