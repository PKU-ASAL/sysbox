package substrate

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGuestFamilyAndShellKindValidation(t *testing.T) {
	require.Equal(t, GuestFamily("linux"), GuestFamilyLinux)
	require.Equal(t, GuestFamily("windows"), GuestFamilyWindows)
	require.Equal(t, GuestFamily("unknown"), GuestFamilyUnknown)
	for _, family := range []GuestFamily{GuestFamilyLinux, GuestFamilyWindows, GuestFamilyUnknown} {
		require.NoError(t, ValidateGuestFamily(family))
	}
	require.ErrorContains(t, ValidateGuestFamily("darwin"), "guest family")

	for _, shell := range []ShellKind{ShellNone, ShellLinux, ShellPowerShell, ShellCmd} {
		require.NoError(t, ValidateShellKind(shell))
	}
	require.ErrorContains(t, ValidateShellKind("bash"), "shell kind")
}

func TestExecRequestValidateAndClone(t *testing.T) {
	req := ExecRequest{
		Program:     "/usr/bin/env",
		Args:        []string{"one"},
		Environment: map[string]string{"MODE": "test"},
		WorkingDir:  "/tmp",
		Shell:       ShellNone,
		Stdin:       strings.NewReader("input"),
	}
	require.NoError(t, req.Validate())
	clone := req.Clone()
	clone.Args[0] = "two"
	clone.Environment["MODE"] = "changed"
	require.Equal(t, "one", req.Args[0])
	require.Equal(t, "test", req.Environment["MODE"])
	require.Same(t, req.Stdin, clone.Stdin)

	require.ErrorContains(t, (ExecRequest{Shell: ShellNone}).Validate(), "program")
	require.ErrorContains(t, (ExecRequest{Program: "true", Shell: ShellNone, Environment: map[string]string{"A; touch /tmp/pwn": "x"}}).Validate(), "environment key")
}
