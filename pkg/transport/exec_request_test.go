package transport

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/substrate"
)

func TestCommandArgvPreservesStructuredExecution(t *testing.T) {
	tests := []struct {
		name string
		req  substrate.ExecRequest
		want []string
	}{
		{"direct", substrate.ExecRequest{Program: "/bin/echo", Args: []string{"a b"}, Shell: substrate.ShellNone}, []string{"/bin/echo", "a b"}},
		{"linux", substrate.ExecRequest{Program: "printf '%s' \"$1\"", Args: []string{"value"}, Shell: substrate.ShellLinux}, []string{"sh", "-c", "printf '%s' \"$1\"", "--", "value"}},
		{"powershell", substrate.ExecRequest{Program: "Write-Output $args[0]", Args: []string{"value"}, Shell: substrate.ShellPowerShell}, []string{"powershell", "-NoProfile", "-NonInteractive", "-Command", "Write-Output $args[0]", "value"}},
		{"cmd", substrate.ExecRequest{Program: "echo %1", Args: []string{"value"}, Shell: substrate.ShellCmd}, []string{"cmd.exe", "/S", "/C", "echo %1", "value"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CommandArgv(tt.req)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestRemoteCommandQuotesEnvironmentWorkingDirectoryAndArguments(t *testing.T) {
	command, err := RemoteCommand(substrate.ExecRequest{
		Program: "/bin/echo", Args: []string{"a b", "$(touch /tmp/no)"},
		Environment: map[string]string{"Z": "last", "A": "one two"},
		WorkingDir:  "/tmp/a b", Shell: substrate.ShellNone,
	})
	require.NoError(t, err)
	require.Equal(t, "cd '/tmp/a b' && A='one two' Z='last' '/bin/echo' 'a b' '$(touch /tmp/no)'", command)
}
