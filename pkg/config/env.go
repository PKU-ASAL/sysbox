package config

const (
	DefaultHomeDir  = "/var/lib/sysbox"
	DefaultCacheDir = "/var/cache/sysbox"
)

// SysboxCache returns the shared artifact cache root.
func SysboxCache() string {
	return MustLoadServiceConfig("").Paths.Cache
}

func DefaultWorkspacesDir() string {
	return MustLoadServiceConfig("").Paths.WorkspacesDir
}

func DefaultRunsDir() string {
	return MustLoadServiceConfig("").Paths.RunsDir
}

func FirecrackerWorkDir() string {
	return MustLoadServiceConfig("").Providers.Firecracker.Workdir
}
