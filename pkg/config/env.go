package config

const (
	DefaultHomeDir  = "/var/lib/sysbox"
	DefaultCacheDir = "/var/cache/sysbox"
)

// SysboxHome returns the service data root. API deployments should mount this
// as persistent storage; CLI users can leave it unset and use explicit flags.
func SysboxHome() string {
	return MustLoadServiceConfig("").Paths.Home
}

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
