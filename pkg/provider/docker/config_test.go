package docker

import (
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/stretchr/testify/require"
)

func decodeDockerTestConfig(t *testing.T, source string) (*Config, error) {
	t.Helper()
	file, diags := hclsyntax.ParseConfig([]byte(source), "provider.hcl", hcl.Pos{Line: 1, Column: 1})
	require.False(t, diags.HasErrors(), diags.Error())
	sub, err := New()
	require.NoError(t, err)
	decoded, err := sub.DecodeProviderConfig(file.Body, nil)
	if err != nil {
		return nil, err
	}
	return decoded.(*Config), nil
}

func TestDecodeProviderConfigPreservesLaunchOverridePresence(t *testing.T) {
	cfg, err := decodeDockerTestConfig(t, `privileged = true`)
	require.NoError(t, err)
	require.False(t, cfg.Entrypoint.Set)
	require.False(t, cfg.Command.Set)

	cfg, err = decodeDockerTestConfig(t, `
entrypoint = []
command = ["mongod", "--bind_ip", "0.0.0.0"]
`)
	require.NoError(t, err)
	require.True(t, cfg.Entrypoint.Set)
	require.Empty(t, cfg.Entrypoint.Value)
	require.True(t, cfg.Command.Set)
	require.Equal(t, []string{"mongod", "--bind_ip", "0.0.0.0"}, cfg.Command.Value)
}

func TestDecodeProviderConfigRejectsShellFormLaunchValues(t *testing.T) {
	_, err := decodeDockerTestConfig(t, `command = "mongod --bind_ip 0.0.0.0"`)
	require.ErrorContains(t, err, "command")
}

func TestEffectiveLaunchInheritsOverridesAndClearsImageValues(t *testing.T) {
	imageEntrypoint := []string{"/entry"}
	imageCommand := []string{"serve", "--port", "80"}
	tests := []struct {
		name           string
		config         *Config
		wantEntrypoint []string
		wantCommand    []string
	}{
		{name: "inherit", config: &Config{}, wantEntrypoint: imageEntrypoint, wantCommand: imageCommand},
		{name: "entrypoint only", config: &Config{Entrypoint: OptionalArgv{Set: true, Value: []string{"/override"}}}, wantEntrypoint: []string{"/override"}, wantCommand: imageCommand},
		{name: "command only", config: &Config{Command: OptionalArgv{Set: true, Value: []string{"mongod"}}}, wantEntrypoint: imageEntrypoint, wantCommand: []string{"mongod"}},
		{name: "both", config: &Config{Entrypoint: OptionalArgv{Set: true, Value: []string{"/override"}}, Command: OptionalArgv{Set: true, Value: []string{"serve"}}}, wantEntrypoint: []string{"/override"}, wantCommand: []string{"serve"}},
		{name: "clear entrypoint", config: &Config{Entrypoint: OptionalArgv{Set: true, Value: []string{}}}, wantEntrypoint: []string{}, wantCommand: imageCommand},
		{name: "clear command", config: &Config{Command: OptionalArgv{Set: true, Value: []string{}}}, wantEntrypoint: imageEntrypoint, wantCommand: []string{}},
		{name: "empty effective argv", config: &Config{Entrypoint: OptionalArgv{Set: true, Value: []string{}}, Command: OptionalArgv{Set: true, Value: []string{}}}, wantEntrypoint: []string{}, wantCommand: []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entrypoint, command := effectiveLaunch(imageEntrypoint, imageCommand, tt.config)
			require.Equal(t, tt.wantEntrypoint, entrypoint)
			require.Equal(t, tt.wantCommand, command)
		})
	}

	entrypoint, _ := effectiveLaunch(imageEntrypoint, imageCommand, &Config{})
	entrypoint[0] = "changed"
	require.Equal(t, "/entry", imageEntrypoint[0])
}
