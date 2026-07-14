package runtime

import (
	"fmt"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/substrate"
)

func resolveGuestFamily(image, override substrate.GuestFamily) (substrate.GuestFamily, error) {
	if err := substrate.ValidateGuestFamily(image); err != nil {
		return "", err
	}
	if override == "" {
		return image, nil
	}
	if err := substrate.ValidateGuestFamily(override); err != nil {
		return "", err
	}
	if image != substrate.GuestFamilyUnknown && image != override {
		return "", fmt.Errorf("node guest family %q conflicts with image guest family %q", override, image)
	}
	return override, nil
}

func validateGuestFamilies(g *graph.Graph) error {
	for _, node := range g.All() {
		cfg, ok := node.Data.(*config.NodeConfig)
		if !ok {
			continue
		}
		imageAddress, err := config.ResolveResourceAddress(cfg.Image, "sysbox_image")
		if err != nil {
			return fmt.Errorf("node %s: %w", node.Address, err)
		}
		for _, dependency := range node.Deps {
			if dependency.Type == imageAddress.Type && dependency.Name == imageAddress.Name {
				imageAddress = dependency
				break
			}
		}
		imageNode := g.Get(imageAddress)
		if imageNode == nil {
			return fmt.Errorf("node %s: image %s is not declared", node.Address, imageAddress)
		}
		imageConfig, ok := imageNode.Data.(*config.ImageConfig)
		if !ok {
			return fmt.Errorf("node %s: image %s has invalid configuration", node.Address, imageAddress)
		}
		family, err := resolveGuestFamily(substrate.GuestFamily(imageConfig.GuestFamily), substrate.GuestFamily(cfg.GuestFamily))
		if err != nil {
			return fmt.Errorf("node %s: %w", node.Address, err)
		}
		if family != substrate.GuestFamilyUnknown {
			continue
		}
		if len(cfg.Provisioners) > 0 {
			return fmt.Errorf("node %s: unknown guest family requires an explicit override before provisioning", node.Address)
		}
		substrateName, resolveErr := resolveSubstrateRef(cfg.Substrate)
		if resolveErr != nil {
			continue
		}
		if descriptor, exists := driver.DefaultRegistry.Get(substrateName); exists && descriptor.GuestNetworkInit != nil && len(cfg.Links) > 0 {
			return fmt.Errorf("node %s: unknown guest family requires an explicit override before guest network initialization", node.Address)
		}
	}
	return nil
}
