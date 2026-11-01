package hostsupervisor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/oslab/sysbox/pkg/config"
)

const HostToolPath = "/opt/sysbox/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// HostEnvironment is a complete environment, never an extension of the
// invoking shell. Only the administrator's file selects the daemon endpoint.
func HostEnvironment(path string, cfg config.ServiceConfig) ([]string, error) {
	socket := cfg.HostSupervisor.DockerSocket
	if socket == "" {
		socket = "/var/run/docker.sock"
	}
	if !filepath.IsAbs(socket) || filepath.Clean(socket) != socket || strings.ContainsAny(socket, "\x00\n\r") {
		return nil, fmt.Errorf("absolute local Docker socket required")
	}
	return []string{"PATH=" + HostToolPath, "LANG=C", "LC_ALL=C", "HOME=/var/lib/sysbox-host", "SYSBOX_CONFIG=" + path, "DOCKER_HOST=unix://" + socket, "LIBVIRT_DEFAULT_URI=qemu:///system"}, nil
}

// ValidateHostTools resolves only from the fixed host PATH, then verifies the
// actual executable and all ancestors. Root-owned distro symlinks are allowed.
func ValidateHostTools(cfg config.ServiceConfig) error {
	for _, dir := range filepath.SplitList(HostToolPath) {
		resolved, err := trustedHostToolPath(dir, 0)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if err = ValidateOwnedPath(resolved, false); err != nil {
			return err
		}
	}
	names := []string{"ip", "nsenter", "nft", "sysbox-netns"}
	if cfg.Providers.Docker.Enabled {
		names = append(names, "docker", "iptables")
	}
	if cfg.Providers.Firecracker.Enabled {
		names = append(names, "mkfs.ext4", "debugfs", "docker")
	}
	if cfg.Providers.Libvirt.Enabled {
		names = append(names, "virsh", "qemu-img", "genisoimage", "ssh-keygen")
	}
	for _, name := range names {
		path, err := exec.LookPath(name)
		if err != nil {
			return fmt.Errorf("host tool %s: %w", name, err)
		}
		path, err = trustedHostToolPath(path, 0)
		if err != nil {
			return err
		}
		if err = ValidateOwnedPath(path, false); err != nil {
			return fmt.Errorf("host tool %s: %w", name, err)
		}
		st, err := os.Stat(path)
		if err != nil {
			return err
		}
		if !st.Mode().IsRegular() || st.Mode().Perm()&0111 == 0 {
			return fmt.Errorf("host tool %s must be executable", name)
		}
	}
	return nil
}

func trustedHostToolPath(path string, depth int) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || depth > 40 {
		return "", fmt.Errorf("invalid host tool path")
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	current := "/"
	for i, part := range parts {
		current = filepath.Join(current, part)
		st, err := os.Lstat(current)
		if err != nil {
			return "", err
		}
		if st.Mode()&os.ModeSymlink != 0 {
			owner, ok := st.Sys().(*syscall.Stat_t)
			if !ok || (owner.Uid != 0 && owner.Uid != uint32(os.Geteuid())) {
				return "", fmt.Errorf("untrusted host tool symlink")
			}
			target, err := os.Readlink(current)
			if err != nil {
				return "", err
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(current), target)
			}
			return trustedHostToolPath(filepath.Join(append([]string{target}, parts[i+1:]...)...), depth+1)
		}
		if err := ValidateOwnedPath(current, false); err != nil {
			return "", err
		}
	}
	return path, nil
}
