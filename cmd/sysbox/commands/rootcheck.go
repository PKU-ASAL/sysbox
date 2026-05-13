package commands

import (
	"fmt"
	"os"
)

// requireRoot exits if not running as root.
// Network namespace creation requires CAP_SYS_ADMIN + write access to
// /run/netns/ — both effectively require uid 0.
func requireRoot() {
	if os.Getuid() != 0 {
		fmt.Fprintln(os.Stderr,
			"error: sysbox apply/destroy require root (netns creation + /run/netns/ write).\n"+
				"  Run: sudo -E sysbox apply ...")
		os.Exit(1)
	}
}
