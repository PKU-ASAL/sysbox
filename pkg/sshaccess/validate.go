package sshaccess

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/oslab/sysbox/pkg/substrate"
)

// SSHDProbe describes one real connection class and the administrator-owned
// key-file setting that must remain unchanged. All Match-dependent classes
// must be supplied by the deployment adapter, including unmanaged SSH users.
type SSHDProbe struct {
	User               string
	RemoteAddress      string
	RemoteHost         string
	LocalAddress       string
	LocalPort          int
	AuthorizedKeysFile string
}

type SSHDRunner func(context.Context, ...string) ([]byte, error)

var probeHostname = regexp.MustCompile(`^[A-Za-z0-9_.:-]+$`)

// ValidateSSHD checks a complete staged config, never just the generated text.
// The runner must execute a trusted sshd binary with bounded output/deadline,
// without a shell. This validates only supplied probes: it is neither a full
// administrator-config equivalence proof nor proof a live daemon was reloaded.
func ValidateSSHD(ctx context.Context, run SSHDRunner, configPath, helper, commandUser string, configs []substrate.SSHAccessConfig, probes []SSHDProbe) error {
	if run == nil || !filepath.IsAbs(configPath) || filepath.Clean(configPath) != configPath || strings.ContainsRune(configPath, 0) || len(probes) == 0 || len(probes) > 1024 {
		return fmt.Errorf("complete SSH config and bounded connection probes required")
	}
	if _, err := RenderSSHD(helper, commandUser, configs); err != nil {
		return err
	}
	for _, probe := range probes {
		if substrate.ValidateSSHAccessUser(probe.User) != nil || !probeHostname.MatchString(probe.RemoteHost) ||
			net.ParseIP(probe.RemoteAddress) == nil || net.ParseIP(probe.LocalAddress) == nil || probe.LocalPort < 1 || probe.LocalPort > 65535 ||
			strings.TrimSpace(probe.AuthorizedKeysFile) != probe.AuthorizedKeysFile || probe.AuthorizedKeysFile == "" || strings.ContainsAny(probe.AuthorizedKeysFile, "\r\n\x00") {
			return fmt.Errorf("invalid SSH effective-config probe")
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := run(ctx, "-t", "-f", configPath); err != nil {
		return fmt.Errorf("SSH syntax validation: %w", err)
	}
	for _, probe := range probes {
		if err := ctx.Err(); err != nil {
			return err
		}
		spec := fmt.Sprintf("user=%s,host=%s,addr=%s,laddr=%s,lport=%d", probe.User, probe.RemoteHost, probe.RemoteAddress, probe.LocalAddress, probe.LocalPort)
		raw, err := run(ctx, "-T", "-f", configPath, "-C", spec)
		if err != nil {
			return fmt.Errorf("SSH effective config for %s: %w", spec, err)
		}
		fields, err := sshdKeySettings(raw)
		if err != nil {
			return err
		}
		expected := expectedSSHCommand(helper, configs, probe.LocalAddress, probe.LocalPort)
		if fields["authorizedkeyscommand"] != expected || fields["authorizedkeyscommanduser"] != commandUser || fields["authorizedkeysfile"] != probe.AuthorizedKeysFile {
			return fmt.Errorf("SSH effective key configuration conflict for %s", spec)
		}
	}
	return ctx.Err()
}

func expectedSSHCommand(helper string, configs []substrate.SSHAccessConfig, ip string, port int) string {
	addressMatch, portMatch := false, false
	for _, cfg := range configs {
		addressMatch = addressMatch || cfg.BindIP != "" && net.ParseIP(cfg.BindIP).Equal(net.ParseIP(ip))
		portMatch = portMatch || cfg.Port == port
	}
	base := helper + " --ssh-authorized-keys %u"
	switch {
	case addressMatch && portMatch:
		return fmt.Sprintf("%s %s %d", base, net.ParseIP(ip).String(), port)
	case addressMatch:
		return base + " " + net.ParseIP(ip).String() + " 0"
	case portMatch:
		return fmt.Sprintf("%s - %d", base, port)
	default:
		return base
	}
}

func sshdKeySettings(raw []byte) (map[string]string, error) {
	if len(raw) > 1<<20 {
		return nil, fmt.Errorf("oversized SSH effective config")
	}
	fields := make(map[string]string)
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		name, value, found := strings.Cut(scanner.Text(), " ")
		if name != "authorizedkeyscommand" && name != "authorizedkeyscommanduser" && name != "authorizedkeysfile" {
			continue
		}
		if !found || fields[name] != "" || strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("ambiguous SSH effective key setting")
		}
		fields[name] = strings.TrimSpace(value)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(fields) != 3 {
		return nil, fmt.Errorf("incomplete SSH effective key settings")
	}
	return fields, nil
}
