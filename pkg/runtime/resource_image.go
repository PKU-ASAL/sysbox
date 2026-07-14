package runtime

import (
	"context"
	"fmt"

	"github.com/hashicorp/hcl/v2"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/artifact"
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

	kind := substrate.ArtifactKind(cfg.Kind)
	if err := substrate.ValidateArtifactKind(kind); err != nil {
		return state.Resource{}, fmt.Errorf("image %s: %w", n.Address.Name, err)
	}
	family := substrate.GuestFamily(cfg.GuestFamily)
	if err := substrate.ValidateGuestFamily(family); err != nil {
		return state.Resource{}, fmt.Errorf("image %s: %w", n.Address.Name, err)
	}
	resolvedSource, err := secret.ResolveString(ctx, executionSecretResolver, cfg.Source)
	if err != nil {
		return state.Resource{}, fmt.Errorf("image %s source: %w", n.Address.Name, err)
	}
	providerSource := resolvedSource
	resolvedSHA := cfg.SHA256
	if kind != substrate.ArtifactOCI {
		r, err := artifact.New().Resolve(artifact.Spec{Source: resolvedSource, SHA256: cfg.SHA256})
		if err != nil {
			return state.Resource{}, fmt.Errorf("image %s source: %w", n.Address.Name, err)
		}
		if r.FromCache {
			pc.Logf("[apply] image %s: cache hit (%s)\n", n.Address.Name, r.Path)
		} else if artifact.IsURL(cfg.Source) {
			pc.Logf("[apply] image %s: fetched to %s\n", n.Address.Name, r.Path)
		}
		providerSource = r.Path
		resolvedSHA = r.SHA256
	}

	ref, err := artifactDriver.PrepareImage(ctx, providerImageSpec(kind, providerSource, cfg.Size))
	if err != nil {
		return state.Resource{}, err
	}

	inst := map[string]any{
		"image_id":     ref.ID,
		"repository":   ref.Repository,
		"kind":         cfg.Kind,
		"source":       cfg.Source,
		"sha256":       resolvedSHA,
		"architecture": cfg.Architecture,
		"guest_family": cfg.GuestFamily,
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

func providerImageSpec(kind substrate.ArtifactKind, source, size string) substrate.ImageSpec {
	spec := substrate.ImageSpec{Size: size}
	switch kind {
	case substrate.ArtifactOCI:
		spec.DockerRef = source
	case substrate.ArtifactRootFS:
		spec.Rootfs = source
	case substrate.ArtifactQCow2:
		spec.QCow2 = source
	}
	return spec
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
