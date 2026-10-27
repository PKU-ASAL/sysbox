package sshaccess

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func deploymentFixture(t *testing.T) (*Deployment, []substrate.SSHAccessConfig, *int) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0700))
	main := filepath.Join(dir, "sshd_config")
	drop := filepath.Join(dir, "sysbox.conf")
	require.NoError(t, os.WriteFile(main, []byte("AuthorizedKeysFile .ssh/authorized_keys\nInclude "+drop+"\n"), 0600))
	configs := []substrate.SSHAccessConfig{{User: "ubuntu", AccessID: "admin", Port: 2222}}
	calls := new(int)
	d := &Deployment{ServiceIdentity: "test-sshd", Root: filepath.Join(dir, "journal"), ConfigPath: main, DropInPath: drop, Helper: "/usr/local/bin/sysbox-guest-task", CommandUser: "root",
		Probes: []SSHDProbe{{User: "ubuntu", RemoteAddress: "192.0.2.1", RemoteHost: "client", LocalAddress: "127.0.0.1", LocalPort: 2222, AuthorizedKeysFile: ".ssh/authorized_keys"}},
		Run: func(ctx context.Context, args ...string) ([]byte, error) {
			raw, err := os.ReadFile(args[2])
			if err != nil {
				return nil, err
			}
			for _, line := range strings.Split(string(raw), "\n") {
				fields := strings.Fields(line)
				if len(fields) == 2 && fields[0] == "Include" {
					fragment, err := os.ReadFile(fields[1])
					if err != nil {
						return nil, err
					}
					raw = append(raw, fragment...)
				}
			}
			if !strings.Contains(string(raw), "AuthorizedKeysCommand ") {
				return nil, errors.New("missing managed lookup")
			}
			if args[0] == "-t" {
				return nil, nil
			}
			return []byte("authorizedkeyscommand /usr/local/bin/sysbox-guest-task --ssh-authorized-keys %u - 2222\nauthorizedkeyscommanduser root\nauthorizedkeysfile .ssh/authorized_keys\n"), nil
		}, Reload: func(context.Context) error { *calls++; return nil }, Ready: func(context.Context) error { return nil },
	}
	return d, configs, calls
}

func TestDeploymentInstallsOnlyOwnedDropIn(t *testing.T) {
	d, configs, calls := deploymentFixture(t)
	before, err := os.ReadFile(d.ConfigPath)
	require.NoError(t, err)
	require.NoError(t, d.Ensure(context.Background(), configs))
	generated, err := RenderSSHD(d.Helper, d.CommandUser, configs)
	require.NoError(t, err)
	actual, err := os.ReadFile(d.DropInPath)
	require.NoError(t, err)
	require.Equal(t, generated, actual)
	after, err := os.ReadFile(d.ConfigPath)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Equal(t, 1, *calls)
}
func TestDeploymentRejectsConflictBeforePublication(t *testing.T) {
	for _, scenario := range []string{"syntax", "existing-file", "symlink", "foreign-command", "missing-include", "assignment-include"} {
		t.Run(scenario, func(t *testing.T) {
			d, configs, calls := deploymentFixture(t)
			switch scenario {
			case "syntax":
				d.Run = func(context.Context, ...string) ([]byte, error) { return nil, errors.New("syntax invalid") }
			case "existing-file":
				require.NoError(t, os.WriteFile(d.DropInPath, []byte("foreign"), 0600))
			case "symlink":
				require.NoError(t, os.Symlink(d.ConfigPath, d.DropInPath))
			case "foreign-command":
				raw, err := os.ReadFile(d.ConfigPath)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(d.ConfigPath, append([]byte("AuthorizedKeysCommand /usr/bin/false\n"), raw...), 0600))
			case "missing-include":
				require.NoError(t, os.WriteFile(d.ConfigPath, []byte("AuthorizedKeysFile .ssh/authorized_keys\n"), 0600))
			case "assignment-include":
				raw, err := os.ReadFile(d.ConfigPath)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(d.ConfigPath, append([]byte("Include=/untracked/config\n"), raw...), 0600))
			}
			require.Error(t, d.Ensure(context.Background(), configs))
			require.Zero(t, *calls)
			if scenario == "existing-file" {
				raw, err := os.ReadFile(d.DropInPath)
				require.NoError(t, err)
				require.Equal(t, "foreign", string(raw))
			}
		})
	}
}

func TestDeploymentRecoveryRejectsChangedService(t *testing.T) {
	d, configs, calls := deploymentFixture(t)
	require.NoError(t, d.Ensure(context.Background(), configs))
	cold := *d
	cold.ServiceIdentity = "other-service"
	require.Error(t, cold.Recover(context.Background()))
	require.Equal(t, 1, *calls)
}
func TestDeploymentColdRecoveryRetriesReload(t *testing.T) {
	d, configs, calls := deploymentFixture(t)
	d.Reload = func(context.Context) error { *calls++; return errors.New("reload interrupted") }
	require.ErrorContains(t, d.Ensure(context.Background(), configs), "reload interrupted")
	require.Equal(t, 1, *calls)
	cold := *d
	cold.Reload = func(context.Context) error { *calls++; return nil }
	require.NoError(t, cold.Recover(context.Background()))
	require.Equal(t, 2, *calls)
}
func TestDeploymentRecoveryPreservesAdministratorEdit(t *testing.T) {
	d, configs, calls := deploymentFixture(t)
	d.Reload = func(context.Context) error { *calls++; return errors.New("interrupted") }
	require.Error(t, d.Ensure(context.Background(), configs))
	require.NoError(t, os.WriteFile(d.DropInPath, []byte("administrator edit"), 0600))
	require.Error(t, d.Recover(context.Background()))
	require.Equal(t, 1, *calls)
	raw, err := os.ReadFile(d.DropInPath)
	require.NoError(t, err)
	require.Equal(t, "administrator edit", string(raw))
}

func TestDeploymentRecoveryRejectsRemovedInstalledDropIn(t *testing.T) {
	d, configs, calls := deploymentFixture(t)
	require.NoError(t, d.Ensure(context.Background(), configs))
	require.NoError(t, os.Remove(d.DropInPath))
	require.Error(t, d.Recover(context.Background()))
	require.Equal(t, 1, *calls)
	_, err := os.Stat(d.DropInPath)
	require.ErrorIs(t, err, os.ErrNotExist)
}
func TestDeploymentRejectsConfigurationChangedDuringReload(t *testing.T) {
	d, configs, _ := deploymentFixture(t)
	d.Reload = func(context.Context) error {
		return os.WriteFile(d.ConfigPath, []byte("administrator changed config"), 0600)
	}
	require.Error(t, d.Ensure(context.Background(), configs))
}

func TestDeploymentDoesNotOverwriteEditDuringValidation(t *testing.T) {
	d, configs, _ := deploymentFixture(t)
	require.NoError(t, d.Ensure(context.Background(), configs))
	runner := d.Run
	calls := 0
	d.Run = func(ctx context.Context, args ...string) ([]byte, error) {
		calls++
		if calls == 3 {
			require.NoError(t, os.WriteFile(d.DropInPath, []byte("administrator edit"), 0600))
		}
		return runner(ctx, args...)
	}
	require.Error(t, d.Ensure(context.Background(), configs))
	raw, err := os.ReadFile(d.DropInPath)
	require.NoError(t, err)
	require.Equal(t, "administrator edit", string(raw))
}

func TestDeploymentDoesNotReloadChangedInstalledConfig(t *testing.T) {
	d, configs, calls := deploymentFixture(t)
	runner := d.Run
	d.Run = func(ctx context.Context, args ...string) ([]byte, error) {
		result, err := runner(ctx, args...)
		if args[0] == "-T" && args[2] == d.ConfigPath {
			require.NoError(t, os.WriteFile(d.DropInPath, []byte("administrator edit"), 0600))
		}
		return result, err
	}
	require.Error(t, d.Ensure(context.Background(), configs))
	require.Zero(t, *calls)
}
