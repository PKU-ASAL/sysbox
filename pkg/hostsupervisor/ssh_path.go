package hostsupervisor

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ManagedSSHPath derives the only guest path used by host-managed SSH access.
// The username comes from the canonical provider handle; callers must not
// substitute a request-provided path or silently fall back to another user.
func ManagedSSHPath(user, accessID string) (string, error) {
	if user == "" || accessID == "" || user == "." || user == ".." || accessID == "." || accessID == ".." || strings.ContainsAny(user+accessID, "/\\\x00") {
		return "", fmt.Errorf("invalid managed SSH identity")
	}
	path := filepath.Join("/home", user, ".ssh", "authorized_keys.d", "sysbox-"+accessID)
	if filepath.Clean(path) != path || !strings.HasPrefix(path, filepath.Join("/home", user, ".ssh", "authorized_keys.d")+string(filepath.Separator)+"sysbox-") {
		return "", fmt.Errorf("invalid managed SSH path")
	}
	return path, nil
}
