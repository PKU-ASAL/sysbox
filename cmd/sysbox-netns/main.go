// Command sysbox-netns runs an `nft` subcommand inside a target network
// namespace. It is a thin shim over `nsenter` so the firewall's netns access
// runs in a killable subprocess: the agent bounds each call with a timeout and
// can kill it without a wedged nft dump hanging the agent process.
//
// Usage:
//
//	sysbox-netns <netns-path> <nft-args...>
//
// The netns path is either a named namespace (/var/run/netns/<name>) or a
// process namespace (/proc/<pid>/ns/net), or an inherited FD (/proc/self/fd/3).
// The helper pins it before forwarding the FD to nsenter. An `nft` script can be piped via stdin
// for `-f -`:
//
//	echo 'add table ip t' | sysbox-netns /proc/123/ns/net -f -
package main

import (
	"io"
	"os"
	"os/exec"
	"syscall"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 && (args[0] == "--anchor-create" || args[0] == "--anchor-delete" || args[0] == "--anchor-local") {
		return runAnchor(args, stdout, stderr)
	}
	if len(args) < 2 {
		_, _ = io.WriteString(stderr, "usage: sysbox-netns <netns-path> <nft-args...>\n")
		return 2
	}
	netnsPath := args[0]
	nftArgs := args[1:]

	ns, err := os.Open(netnsPath)
	if err != nil {
		_, _ = io.WriteString(stderr, "open namespace: "+err.Error()+"\n")
		return 1
	}
	defer ns.Close()
	cmd := nsenterCommand("/proc/self/fd/3", nftArgs)
	cmd.ExtraFiles = []*os.File{ns}
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		return 1
	}
	return 0
}

// nsenterCommand builds the `nsenter --net=<path> nft <args...>` invocation.
// `--net` is an optional-argument long option, so the value must use the `=`
// form. Pdeathsig propagates SIGKILL to the child when the helper is itself
// killed by the agent's timeout, so a wedged nft dump cannot outlive its caller.
func nsenterCommand(netnsPath string, nftArgs []string) *exec.Cmd {
	cmd := exec.Command("nsenter", append([]string{"--net=" + netnsPath, "nft"}, nftArgs...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	return cmd
}
