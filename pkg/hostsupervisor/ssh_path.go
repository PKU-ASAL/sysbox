package hostsupervisor

import "github.com/oslab/sysbox/pkg/controlplane"

// ManagedSSHPath derives the common owned access-file path contract.
func ManagedSSHPath(user, accessID string) (string, error) {
	return controlplane.ManagedSSHPath(user, accessID)
}
