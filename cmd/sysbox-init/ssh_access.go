package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
)

type sshKeySource interface {
	SSHAuthorizedKeys(context.Context, string, string, int) ([]byte, error)
}

// sshd supplies %u. Its AuthorizedKeysCommand supports neither %l nor %p.
// Optional endpoint arguments must come from administrator-controlled listener
// configuration; unknown endpoints cannot authorize endpoint-restricted keys.
func writeSSHAuthorizedKeys(ctx context.Context, source sshKeySource, args []string, output io.Writer) error {
	if len(args) != 1 && len(args) != 2 && len(args) != 3 {
		return fmt.Errorf("SSH authorized keys requires user and optional endpoint")
	}
	port := uint64(0)
	if len(args) > 1 {
		var err error
		port, err = strconv.ParseUint(args[len(args)-1], 10, 16)
		if err != nil || port == 0 && len(args) != 3 {
			return fmt.Errorf("invalid SSH listener port")
		}
	}
	ip := ""
	if len(args) == 3 {
		ip = args[1]
		if ip == "-" {
			ip = ""
		} else if net.ParseIP(ip) == nil {
			return fmt.Errorf("invalid SSH listener address")
		}
	}
	keys, err := source.SSHAuthorizedKeys(ctx, args[0], ip, int(port))
	if err != nil {
		return err
	}
	_, err = output.Write(keys)
	return err
}
