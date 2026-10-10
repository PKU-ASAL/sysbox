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
	for _, args := range [][]string{{}, {"ubuntu", "127.0.0.1", "0"}, {"ubuntu", "127.0.0.1", "65536"}, {"ubuntu", "127.0.0.1", "22", "extra"}} {
		source := &testSSHKeySource{}
		var output bytes.Buffer
		require.Error(t, writeSSHAuthorizedKeys(context.Background(), source, args, &output))
		require.Zero(t, source.calls)
		require.Empty(t, output.String())
	}
}
