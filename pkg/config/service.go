package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const DefaultConfigPath = "/etc/sysbox/sysbox.yaml"

type ServiceConfig struct {
	Version    int              `yaml:"version" json:"version"`
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
	Docker        ProviderConfig    `yaml:"docker" json:"docker"`
	Network       ProviderConfig    `yaml:"network" json:"network"`
	Libvirt       ProviderConfig    `yaml:"libvirt" json:"libvirt"`
	Firecracker   FirecrackerConfig `yaml:"firecracker" json:"firecracker"`
	DefaultPolicy ProviderPolicy    `yaml:"default_policy" json:"default_policy"`
	Capabilities  map[string]bool   `yaml:"capabilities" json:"capabilities,omitempty"`
}

type FirecrackerConfig struct {
	ProviderConfig `yaml:",inline" json:",inline"`
	Binary         string `yaml:"binary" json:"binary,omitempty"`
	Kernel         string `yaml:"kernel" json:"kernel,omitempty"`
	Workdir        string `yaml:"workdir" json:"workdir,omitempty"`
}

type ProviderConfig struct {
	Enabled bool           `yaml:"enabled" json:"enabled"`
	Policy  ProviderPolicy `yaml:"policy" json:"policy,omitempty"`
}

type ProviderPolicy struct {
	Preflight string `yaml:"preflight" json:"preflight,omitempty"`
}

type ArtifactsConfig struct {
	Roots      []string           `yaml:"roots" json:"roots,omitempty"`
	Registries []ArtifactRegistry `yaml:"registries" json:"registries,omitempty"`
	Policy     ArtifactPolicy     `yaml:"policy" json:"policy,omitempty"`
}

type ArtifactRegistry struct {
	Name string `yaml:"name" json:"name"`
	URI  string `yaml:"uri" json:"uri"`
}

type ArtifactPolicy struct {
	CacheMode string `yaml:"cache_mode" json:"cache_mode,omitempty"`
	Verify    string `yaml:"verify" json:"verify,omitempty"`
}

func DefaultServiceConfig() ServiceConfig {
	return ServiceConfig{
		Version: 1,
		API:     APIConfig{Listen: ":9876"},
		Paths: PathsConfig{
			Home:  DefaultHomeDir,
			Cache: DefaultCacheDir,
		},
		Supervisor: SupervisorConfig{
			Policy:   "observe_only",
			Interval: "30s",
		},
		Providers: ProvidersConfig{
			Docker:        ProviderConfig{Enabled: true},
			Network:       ProviderConfig{Enabled: true},
			Firecracker:   FirecrackerConfig{ProviderConfig: ProviderConfig{Enabled: true}},
			Libvirt:       ProviderConfig{Enabled: true},
			DefaultPolicy: ProviderPolicy{Preflight: "warn"},
			Capabilities:  map[string]bool{},
		},
		Artifacts: ArtifactsConfig{
			Policy: ArtifactPolicy{CacheMode: "on_demand", Verify: "warn"},
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
			dec := yaml.NewDecoder(bytes.NewReader(raw))
			dec.KnownFields(true)
			if err := dec.Decode(&cfg); err != nil {
				return cfg, fmt.Errorf("decode sysbox config %s: %w", path, err)
			}
		}
	}
	applyEnvOverrides(&cfg)
	applyDerivedDefaults(&cfg)
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
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
	set(&c.API.Token, "SYSBOX_API_TOKEN")
	set(&c.State.Backend, "SYSBOX_STATE_BACKEND")
	set(&c.Providers.Firecracker.Binary, "SYSBOX_PROVIDER_FIRECRACKER_BIN")
	set(&c.Providers.Firecracker.Kernel, "SYSBOX_PROVIDER_FIRECRACKER_KERNEL")
	set(&c.Providers.Firecracker.Workdir, "SYSBOX_PROVIDER_FIRECRACKER_WORKDIR")
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
	if c.Version == 0 {
		c.Version = 1
	}
	if c.Providers.DefaultPolicy.Preflight == "" {
		c.Providers.DefaultPolicy.Preflight = "warn"
	}
	if c.Artifacts.Policy.CacheMode == "" {
		c.Artifacts.Policy.CacheMode = "on_demand"
	}
	if c.Artifacts.Policy.Verify == "" {
		c.Artifacts.Policy.Verify = "warn"
	}
}

func (c ServiceConfig) Validate() error {
	if c.Version != 1 {
		return fmt.Errorf("unsupported sysbox config version %d", c.Version)
	}
	if strings.TrimSpace(c.API.Listen) == "" {
		return fmt.Errorf("api.listen is required")
	}
	if _, err := time.ParseDuration(c.Supervisor.Interval); err != nil && c.Supervisor.Interval != "0" && c.Supervisor.Interval != "off" && c.Supervisor.Interval != "disabled" {
		return fmt.Errorf("supervisor.interval: %w", err)
	}
	switch c.Supervisor.Policy {
	case "", "observe_only", "restart_on_crash":
	default:
		return fmt.Errorf("supervisor.policy must be observe_only or restart_on_crash")
	}
	switch c.Providers.DefaultPolicy.Preflight {
	case "", "ignore", "warn", "error":
	default:
		return fmt.Errorf("providers.default_policy.preflight must be ignore, warn, or error")
	}
	switch c.Artifacts.Policy.CacheMode {
	case "", "on_demand", "readonly", "disabled":
	default:
		return fmt.Errorf("artifacts.policy.cache_mode must be on_demand, readonly, or disabled")
	}
	switch c.Artifacts.Policy.Verify {
	case "", "warn", "strict", "disabled":
	default:
		return fmt.Errorf("artifacts.policy.verify must be warn, strict, or disabled")
	}
	for _, root := range c.Artifacts.Roots {
		if strings.TrimSpace(root) == "" {
			return fmt.Errorf("artifacts.roots cannot contain empty paths")
		}
	}
	for _, reg := range c.Artifacts.Registries {
		if reg.Name == "" || reg.URI == "" {
			return fmt.Errorf("artifacts.registries entries require name and uri")
		}
	}
	return nil
}
