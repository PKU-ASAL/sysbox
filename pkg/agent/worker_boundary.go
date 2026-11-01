package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func ValidateWorkerPrivileges() error {
	if os.Geteuid() == 0 {
		return fmt.Errorf("Agent must run as a non-root user; provider access belongs to host-supervisor")
	}
	raw, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return err
	}
	return validateWorkerStatus(string(raw))
}

func validateWorkerStatus(status string) error {
	fields := map[string]string{}
	for _, line := range strings.Split(status, "\n") {
		parts := strings.Fields(line)
		if len(parts) == 2 {
			fields[strings.TrimSuffix(parts[0], ":")] = parts[1]
		}
	}
	for _, name := range []string{"CapEff", "CapPrm", "CapAmb"} {
		if fields[name] != "0000000000000000" {
			return fmt.Errorf("Agent must have no %s capabilities", name)
		}
	}
	if fields["NoNewPrivs"] != "1" {
		return fmt.Errorf("Agent requires no-new-privileges (systemd NoNewPrivileges or container security_opt)")
	}
	return nil
}

// Worker-readable configuration/public keys and socket parents must be rooted
// in administrator-owned paths. The worker can write only its own session data.
func ValidateHostPath(path string, socket bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("absolute canonical host path required")
	}
	leaf := true
	for current := path; ; current = filepath.Dir(current) {
		st, err := os.Lstat(current)
		if err != nil {
			return err
		}
		stat, ok := st.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 || st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("host path must be root-owned without symlinks: %s", current)
		}
		if leaf && socket {
			if st.Mode()&os.ModeSocket == 0 || st.Mode().Perm()&0007 != 0 {
				return fmt.Errorf("supervisor socket must restrict access to its worker group")
			}
		} else {
			if st.Mode().Perm()&0022 != 0 && !(current == "/tmp" && st.Mode()&os.ModeSticky != 0) {
				return fmt.Errorf("worker-writable host path: %s", current)
			}
			if leaf && !st.Mode().IsRegular() || !leaf && !st.IsDir() {
				return fmt.Errorf("unexpected host path type")
			}
		}
		if current == "/" {
			return nil
		}
		leaf = false
	}
}
