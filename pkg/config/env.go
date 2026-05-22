package config

import (
	"os"
	"path/filepath"
)

const (
	DefaultHomeDir  = "/var/lib/sysbox"
	DefaultCacheDir = "/var/cache/sysbox"
)

// SysboxHome returns the service data root. API deployments should mount this
// as persistent storage; CLI users can leave it unset and use explicit flags.
func SysboxHome() string {
	return envOr("SYSBOX_HOME", DefaultHomeDir)
}

// SysboxCache returns the shared artifact cache root.
func SysboxCache() string {
	return envOr("SYSBOX_CACHE", DefaultCacheDir)
}

func DefaultWorkspacesDir() string {
	return filepath.Join(SysboxHome(), "workspaces")
}

func DefaultRunsDir() string {
	return filepath.Join(SysboxHome(), "runs")
}

func FirecrackerWorkDir() string {
	return filepath.Join(SysboxHome(), "firecracker")
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
