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

	res := artifact.New()

	// Resolve disk image sources through the artifact cache (URL or local path).
	rootfs, qcow2 := cfg.Rootfs, cfg.QCow2
	var resolvedSHA string
	for _, entry := range []struct {
		src   string
		label string
		dst   *string
	}{
		{cfg.Rootfs, "rootfs", &rootfs},
		{cfg.QCow2, "qcow2", &qcow2},
	} {
		if entry.src == "" {
			continue
		}
		resolvedSource, err := secret.ResolveString(ctx, executionSecretResolver, entry.src)
		if err != nil {
			return state.Resource{}, fmt.Errorf("image %s %s: %w", n.Address.Name, entry.label, err)
		}
		r, err := res.Resolve(artifact.Spec{Source: resolvedSource, SHA256: cfg.SHA256})
		if err != nil {
			return state.Resource{}, fmt.Errorf("image %s %s: %w", n.Address.Name, entry.label, err)
		}
		if r.FromCache {
			pc.Logf("[apply] image %s: %s cache hit (%s)\n", n.Address.Name, entry.label, r.Path)
		} else if artifact.IsURL(entry.src) {
			pc.Logf("[apply] image %s: %s fetched to %s\n", n.Address.Name, entry.label, r.Path)
		}
		*entry.dst = r.Path
		resolvedSHA = r.SHA256
	}

	ref, err := artifactDriver.PrepareImage(ctx, substrate.ImageSpec{
		DockerRef: cfg.DockerRef,
		Rootfs:    rootfs,
		QCow2:     qcow2,
		Size:      cfg.Size,
	})
	if err != nil {
		return state.Resource{}, err
	}

	inst := map[string]any{
		"image_id":   ref.ID,
		"repository": ref.Repository,
		"source":     cfg.Rootfs + cfg.QCow2,
		"sha256":     resolvedSHA,
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
	for _, item := range []struct {
		name string
		src  string
	}{
		{"image:" + r.Name + ":rootfs", cfg.Rootfs},
		{"image:" + r.Name + ":qcow2", cfg.QCow2},
	} {
		if check := ArtifactPreflightCheck(item.name, item.src, cfg.SHA256); check != nil {
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
