package transport

import (
	"fmt"
	"sort"
	"strings"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/util"
)

func CommandArgv(req substrate.ExecRequest) ([]string, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	switch req.Shell {
	case substrate.ShellNone:
		return append([]string{req.Program}, req.Args...), nil
	case substrate.ShellLinux:
		return append([]string{"sh", "-c", req.Program, "--"}, req.Args...), nil
	case substrate.ShellPowerShell:
		return append([]string{"powershell", "-NoProfile", "-NonInteractive", "-Command", req.Program}, req.Args...), nil
	case substrate.ShellCmd:
		return append([]string{"cmd.exe", "/S", "/C", req.Program}, req.Args...), nil
	default:
		return nil, fmt.Errorf("unsupported shell kind %q", req.Shell)
	}
}

func RemoteCommand(req substrate.ExecRequest) (string, error) {
	argv, err := CommandArgv(req)
	if err != nil {
		return "", err
	}
	parts := make([]string, 0, len(req.Environment)+2)
	keys := make([]string, 0, len(req.Environment))
	for key := range req.Environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		parts = append(parts, key+"="+util.ShellQuote(req.Environment[key]))
	}
	parts = append(parts, util.ShellQuoteJoin(argv))
	command := strings.Join(parts, " ")
	if req.WorkingDir != "" {
		command = "cd " + util.ShellQuote(req.WorkingDir) + " && " + command
	}
	return command, nil
}
