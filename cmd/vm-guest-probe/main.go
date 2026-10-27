// vm-guest-probe is an operator-only diagnostic for this isolated Firecracker lab.
// It connects to the local VM vsock UDS and executes the supplied argv.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/vsockrpc"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: vm-guest-probe VSOCK_UDS COMMAND [ARG...]")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", os.Args[1])
	if err != nil {
		fail(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	if _, err := fmt.Fprintf(conn, "CONNECT %d\n", vsockrpc.DefaultPort); err != nil {
		fail(err)
	}
	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil {
		fail(err)
	}
	if !strings.HasPrefix(line, "OK ") {
		fail(fmt.Errorf("vsock rejected connection: %s", strings.TrimSpace(line)))
	}
	if err := json.NewEncoder(conn).Encode(vsockrpc.Request{Op: vsockrpc.OpExec, Cmd: os.Args[2:]}); err != nil {
		fail(err)
	}
	decoder := json.NewDecoder(reader)
	for {
		var frame vsockrpc.Frame
		if err := decoder.Decode(&frame); err != nil {
			fail(err)
		}
		if _, err := os.Stdout.Write(frame.Stdout); err != nil {
			fail(err)
		}
		if _, err := os.Stderr.Write(frame.Stderr); err != nil {
			fail(err)
		}
		if frame.Done {
			if frame.Error != "" {
				fail(fmt.Errorf("guest: %s", frame.Error))
			}
			if frame.ExitCode != 0 {
				os.Exit(frame.ExitCode)
			}
			return
		}
	}
}

func fail(err error) {
	if err == io.EOF {
		fmt.Fprintln(os.Stderr, "guest closed vsock stream")
	} else {
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(1)
}
