package commands

import (
	"fmt"
	"os"
)

// requireRoot exits with a clear message if the process is not running as root.
// Network namespace and netlink operations need CAP_NET_ADMIN / root.
func requireRoot() {
	if os.Getuid() != 0 {
		fmt.Fprintln(os.Stderr,
			"error: sysbox requires root for netns/netlink operations\n"+
				"       re-run with: sudo -E sysbox ...")
		os.Exit(1)
	}
}
