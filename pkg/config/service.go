package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

const DefaultConfigPath = "/etc/sysbox/sysbox.yaml"

type ServiceConfig struct {
	API        APIConfig        `yaml:"api" json:"api"`
	Paths      PathsConfig      `yaml:"paths" json:"paths"`
	State      StateConfig      `yaml:"state" json:"state"`
	Supervisor SupervisorConfig `yaml:"supervisor" json:"supervisor"`
	Providers  ProvidersConfig  `yaml:"providers" json:"providers"`
	Artifacts  ArtifactsConfig  `yaml:"artifacts" json:"artifacts"`
}

type APIConfig struct {
	Listen string `yaml:"listen" json:"listen"`
	Token  string `yaml:"token" json:"token,omitempty"`
}

type PathsConfig struct {
	Home          string `yaml:"home" json:"home"`
	Cache         string `yaml:"cache" json:"cache"`
	WorkspacesDir string `yaml:"workspaces_dir" json:"workspaces_dir"`
	RunsDir       string `yaml:"runs_dir" json:"runs_dir"`
}

type StateConfig struct {
	Backend string `yaml:"backend" json:"backend,omitempty"`
}

type SupervisorConfig struct {
	Policy   string `yaml:"policy" json:"policy"`
	Interval string `yaml:"interval" json:"interval"`
}

type ProvidersConfig struct {
	Firecracker FirecrackerConfig `yaml:"firecracker" json:"firecracker"`
}

type FirecrackerConfig struct {
	Binary  string `yaml:"binary" json:"binary,omitempty"`
	Kernel  string `yaml:"kernel" json:"kernel,omitempty"`
	Workdir string `yaml:"workdir" json:"workdir,omitempty"`
}

type ArtifactsConfig struct {
	Roots []string `yaml:"roots" json:"roots,omitempty"`
}

func DefaultServiceConfig() ServiceConfig {
	return ServiceConfig{
		API: APIConfig{Listen: ":9876"},
		Paths: PathsConfig{
			Home:  DefaultHomeDir,
			Cache: DefaultCacheDir,
		},
		Supervisor: SupervisorConfig{
			Policy:   "observe_only",
			Interval: "30s",
		},
	}
}

func LoadServiceConfig(path string) (ServiceConfig, error) {
	cfg := DefaultServiceConfig()
	if path == "" {
		path = os.Getenv("SYSBOX_CONFIG")
	}
	if path == "" {
		path = DefaultConfigPath
	}
	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return cfg, fmt.Errorf("read sysbox config %s: %w", path, err)
		}
		if err == nil {
			if err := yaml.Unmarshal(raw, &cfg); err != nil {
				return cfg, fmt.Errorf("decode sysbox config %s: %w", path, err)
			}
		}
	}
	applyEnvOverrides(&cfg)
	applyDerivedDefaults(&cfg)
	return cfg, nil
}

func MustLoadServiceConfig(path string) ServiceConfig {
	cfg, err := LoadServiceConfig(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
		cfg = DefaultServiceConfig()
		applyEnvOverrides(&cfg)
		applyDerivedDefaults(&cfg)
	}
	return cfg
}

func (c ServiceConfig) SupervisorInterval() time.Duration {
	raw := c.Supervisor.Interval
	if raw == "" {
		raw = "30s"
	}
	if raw == "0" || raw == "off" || raw == "disabled" {
		return 0
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 30 * time.Second
	}
	return d
}

func applyEnvOverrides(c *ServiceConfig) {
	set := func(dst *string, key string) {
		if v := os.Getenv(key); v != "" {
			*dst = v
		}
	}
	set(&c.API.Listen, "SYSBOX_API_LISTEN")
	set(&c.API.Token, "SYSBOX_API_TOKEN")
	set(&c.Paths.Home, "SYSBOX_HOME")
	set(&c.Paths.Cache, "SYSBOX_CACHE")
	set(&c.Paths.WorkspacesDir, "SYSBOX_WORKSPACES_DIR")
	set(&c.Paths.RunsDir, "SYSBOX_RUNS_DIR")
	set(&c.State.Backend, "SYSBOX_STATE_BACKEND")
	set(&c.Supervisor.Policy, "SYSBOX_SUPERVISOR_POLICY")
	set(&c.Supervisor.Interval, "SYSBOX_SUPERVISOR_INTERVAL")
	set(&c.Providers.Firecracker.Binary, "SYSBOX_FIRECRACKER_BIN")
	set(&c.Providers.Firecracker.Kernel, "SYSBOX_FIRECRACKER_KERNEL")
	set(&c.Providers.Firecracker.Workdir, "SYSBOX_FIRECRACKER_WORKDIR")
}

func applyDerivedDefaults(c *ServiceConfig) {
	if c.Paths.Home == "" {
		c.Paths.Home = DefaultHomeDir
	}
	if c.Paths.Cache == "" {
		c.Paths.Cache = DefaultCacheDir
	}
	if c.Paths.WorkspacesDir == "" {
		c.Paths.WorkspacesDir = filepath.Join(c.Paths.Home, "workspaces")
	}
	if c.Paths.RunsDir == "" {
		c.Paths.RunsDir = filepath.Join(c.Paths.Home, "runs")
	}
	if c.Providers.Firecracker.Workdir == "" {
		c.Providers.Firecracker.Workdir = filepath.Join(c.Paths.Home, "firecracker")
	}
	if c.Supervisor.Policy == "" {
		c.Supervisor.Policy = "observe_only"
	}
	if c.Supervisor.Interval == "" {
		c.Supervisor.Interval = "30s"
	}
}
