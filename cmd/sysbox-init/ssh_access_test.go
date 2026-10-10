package main

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type testSSHKeySource struct {
	err   error
	calls int
}

type captureSSHSource struct {
	user, ip string
	port     int
}

func (s *captureSSHSource) SSHAuthorizedKeys(_ context.Context, user, ip string, port int) ([]byte, error) {
	s.user, s.ip, s.port = user, ip, port
	return nil, nil
}

func TestSSHAuthorizedKeysCommandParsesPartialMatchEndpoints(t *testing.T) {
	for _, tc := range []struct {
		args []string
		ip   string
		port int
	}{
		{[]string{"ubuntu"}, "", 0},
		{[]string{"ubuntu", "-", "2222"}, "", 2222},
		{[]string{"ubuntu", "127.0.0.1", "0"}, "127.0.0.1", 0},
		{[]string{"ubuntu", "::1", "2222"}, "::1", 2222},
	} {
		source := &captureSSHSource{}
		var output bytes.Buffer
		require.NoError(t, writeSSHAuthorizedKeys(context.Background(), source, tc.args, &output))
		require.Equal(t, "ubuntu", source.user)
		require.Equal(t, tc.ip, source.ip)
		require.Equal(t, tc.port, source.port)
	}
}

func (s *testSSHKeySource) SSHAuthorizedKeys(_ context.Context, user, ip string, port int) ([]byte, error) {
	s.calls++
	if user != "ubuntu" || ip != "127.0.0.1" || port != 2222 {
		return nil, errors.New("wrong endpoint")
	}
	return []byte("public-key\n"), s.err
}

func TestSSHAuthorizedKeysCommandOnlyWritesSuccessfulCompleteLookup(t *testing.T) {
	var output bytes.Buffer
	source := &testSSHKeySource{}
	require.NoError(t, writeSSHAuthorizedKeys(context.Background(), source, []string{"ubuntu", "127.0.0.1", "2222"}, &output))
	require.Equal(t, "public-key\n", output.String())
	output.Reset()
	source.err = errors.New("corrupt record")
	require.Error(t, writeSSHAuthorizedKeys(context.Background(), source, []string{"ubuntu", "127.0.0.1", "2222"}, &output))
	require.Empty(t, output.String())
}

func TestSSHAuthorizedKeysCommandRejectsInvalidArgumentsBeforeLookup(t *testing.T) {
	for _, args := range [][]string{{}, {"ubuntu", "0"}, {"ubuntu", "invalid", "22"}, {"ubuntu", "127.0.0.1", "65536"}, {"ubuntu", "127.0.0.1", "22", "extra"}} {
		source := &testSSHKeySource{}
		var output bytes.Buffer
		require.Error(t, writeSSHAuthorizedKeys(context.Background(), source, args, &output))
		require.Zero(t, source.calls)
		require.Empty(t, output.String())
	}
}
