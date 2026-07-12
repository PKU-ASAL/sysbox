package commands

import (
	"fmt"
	"os"

	"github.com/oslab/sysbox/pkg/config"
)

// needsRoot returns true when the topology requires root (netns + netlink).
// Pure Docker-bridge (NAT) topologies do not need root; only isolated
// linux-bridge networks require netns creation + /run/netns/ writes.
func needsRoot(root *config.Root) bool {
	for _, r := range root.Resources {
		if r.Type == "sysbox_network" {
			cfg := &config.NetworkConfig{}
			ctx, err := config.BuildEvalContext(root)
			if err != nil {
				return true
			}
			if err := config.DecodeResource(&r, cfg, ctx); err != nil {
				return true // can't decode → assume root needed
			}
			if !cfg.NAT {
				return true // isolated bridge → needs netns
			}
		}
	}
	return false
}

// checkRoot checks root requirement based on the topology.
// NAT-only topologies (Docker bridge networks) work without root.
// Returns an error instead of calling os.Exit so callers can properly
// unwind defers and cobra hooks.
func checkRoot(root *config.Root) error {
	if os.Getuid() == 0 {
		return nil
	}
	if needsRoot(root) {
		return fmt.Errorf("this topology uses isolated networks (netns) and requires root.\n  Run: sudo -E sysbox apply ...")
	}
	return nil
}
