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
			ctx := config.BuildEvalContext(root)
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

// requireRoot exits if not running as root.
// Deprecated: use checkRoot instead, which respects NAT-only topologies.
func requireRoot() {
	if os.Getuid() != 0 {
		fmt.Fprintln(os.Stderr,
			"error: sysbox apply/destroy require root (netns creation + /run/netns/ write).\n"+
				"  Run: sudo -E sysbox apply ...")
		os.Exit(1)
	}
}

// checkRoot checks root requirement based on the topology.
// NAT-only topologies (Docker bridge networks) work without root.
func checkRoot(root *config.Root) {
	if os.Getuid() == 0 {
		return
	}
	if needsRoot(root) {
		fmt.Fprintln(os.Stderr,
			"error: this topology uses isolated networks (netns) and requires root.\n"+
				"  Run: sudo -E sysbox apply ...")
		os.Exit(1)
	}
}
