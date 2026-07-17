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

	entrypoint, command := effectiveLaunch(imageEntrypoint, imageCommand, &Config{})
	require.Equal(t, imageEntrypoint, entrypoint)
	require.Equal(t, imageCommand, command)
	entrypoint[0] = "changed"
	require.Equal(t, "/entry", imageEntrypoint[0])

	entrypoint, command = effectiveLaunch(imageEntrypoint, imageCommand, &Config{
		Entrypoint: OptionalArgv{Set: true, Value: []string{}},
		Command:    OptionalArgv{Set: true, Value: []string{"mongod", "--bind_ip", "0.0.0.0"}},
	})
	require.Empty(t, entrypoint)
	require.Equal(t, []string{"mongod", "--bind_ip", "0.0.0.0"}, command)

}
