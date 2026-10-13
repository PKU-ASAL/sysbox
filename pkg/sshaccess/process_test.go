package sshaccess

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSSHDProcessArgumentsBindExactConfiguration(t *testing.T) {
	for _, args := range [][]string{
		{"/usr/sbin/sshd", "-D", "-e", "-f", "/etc/ssh/managed.conf"},
		{"sshd", "-D", "-f/etc/ssh/managed.conf"},
		{"sshd: /usr/sbin/sshd -D -e -f /etc/ssh/managed.conf [listener] 0 of 10-100 startups"},
	} {
		require.NoError(t, validateSSHDArguments(args, "/etc/ssh/managed.conf"))
	}
	for _, args := range [][]string{
		{"sshd", "-D"},
		{"sshd: ubuntu@pts/1"},
		{"sshd", "-D", "-f", "/other"},
		{"sshd", "-D", "-f", "/etc/ssh/managed.conf", "-o", "AuthorizedKeysCommand=/bin/true"},
		{"sshd", "-D", "-f", "/etc/ssh/managed.conf", "-f", "/other"},
	} {
		require.Error(t, validateSSHDArguments(args, "/etc/ssh/managed.conf"))
	}
}
