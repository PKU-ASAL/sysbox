package libvirt

import (
	"fmt"

	providerexec "github.com/oslab/sysbox/pkg/provider/exec"
	"github.com/oslab/sysbox/pkg/substrate"
)

// Connection returns an SSH connection to the VM. The SSHIP is either set
// explicitly in the HCL (via acp_ip or similar) or discovered post-boot.
// If SSHIP is empty, provisioners cannot run via SSH.
func (s *Substrate) Connection(handle substrate.NodeHandle, hints []substrate.ConnectionHint) (substrate.Connection, error) {
	hs := hsFrom(handle)

	host := handle.Net.PrimaryIP
	if hs.SSHIP != "" {
		host = hs.SSHIP
	}
	port := "22"
	user := hs.SSHUser
	pass := hs.SSHPass
	key := hs.SSHKey
	if user == "" {
		user = "root"
	}

	// HCL connection {} hint overrides.
	if len(hints) > 0 {
		h := hints[0]
		if h.Host != "" {
			host = h.Host
		}
		if h.User != "" {
			user = h.User
		}
		if h.Password != "" {
			pass = h.Password
		}
		if h.PrivateKey != "" {
			key = h.PrivateKey
		}
	}

	if host == "" {
		return nil, fmt.Errorf("libvirt: no SSH IP for %s; set ssh_ip in provider block or use a provisioner to configure it", handle.ID)
	}

	return providerexec.NewSSHConnectionWithPort(host, port, user, key, pass), nil
}
