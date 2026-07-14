package substrate

import (
	"fmt"
	"io"
	"regexp"
)

var environmentKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type GuestFamily string

const (
	GuestFamilyLinux   GuestFamily = "linux"
	GuestFamilyWindows GuestFamily = "windows"
	GuestFamilyUnknown GuestFamily = "unknown"
)

func ValidateGuestFamily(family GuestFamily) error {
	switch family {
	case GuestFamilyLinux, GuestFamilyWindows, GuestFamilyUnknown:
		return nil
	default:
		return fmt.Errorf("unsupported guest family %q", family)
	}
}

type ShellKind string

const (
	ShellNone       ShellKind = "none"
	ShellLinux      ShellKind = "linux"
	ShellPowerShell ShellKind = "powershell"
	ShellCmd        ShellKind = "cmd"
)

func ValidateShellKind(shell ShellKind) error {
	switch shell {
	case ShellNone, ShellLinux, ShellPowerShell, ShellCmd:
		return nil
	default:
		return fmt.Errorf("unsupported shell kind %q", shell)
	}
}

type ExecRequest struct {
	Program     string
	Args        []string
	Environment map[string]string
	WorkingDir  string
	Shell       ShellKind
	Stdin       io.Reader
}

func (r ExecRequest) Validate() error {
	if r.Program == "" {
		return fmt.Errorf("exec program is required")
	}
	for key := range r.Environment {
		if !environmentKeyPattern.MatchString(key) {
			return fmt.Errorf("invalid environment key %q", key)
		}
	}
	return ValidateShellKind(r.Shell)
}

func (r ExecRequest) Clone() ExecRequest {
	r.Args = append([]string{}, r.Args...)
	r.Environment = cloneStringMap(r.Environment)
	return r
}

func cloneStringMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
