package commands

import (
	"net"
	"syscall"
)

func peerCred(conn net.Conn) (*syscall.Ucred, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return nil, nil
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return nil, err
	}
	var cred *syscall.Ucred
	var credErr error
	raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	return cred, credErr
}
