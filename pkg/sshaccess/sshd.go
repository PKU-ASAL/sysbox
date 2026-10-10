// Package sshaccess manages the OpenSSH adapter for guest-owned access records.
package sshaccess

import (
	"fmt"
	"net"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/oslab/sysbox/pkg/substrate"
)

var helperPath = regexp.MustCompile(`^/[A-Za-z0-9_./-]+$`)

// RenderSSHD contributes only the managed key lookup. It does not install the
// config, validate an existing administrator command, reload sshd or add any
// listeners. The caller must validate the complete effective configuration.
// Install only at a validated config boundary and check sshd -T -C for
// existing command/Match precedence conflicts and trailing configuration.
// sshd uses the first matching command, so intersections precede partial
// endpoint matches. Unknown endpoint components never authorize restricted keys.
func RenderSSHD(helper, commandUser string, configs []substrate.SSHAccessConfig) ([]byte, error) {
	if !helperPath.MatchString(helper) || filepath.Clean(helper) != helper || helper == "/" || substrate.ValidateSSHAccessUser(commandUser) != nil {
		return nil, fmt.Errorf("invalid SSH helper path or command user")
	}
	addresses, ports := map[string]bool{}, map[int]bool{}
	for _, cfg := range configs {
		if cfg.BindIP != "" {
			ip := net.ParseIP(cfg.BindIP)
			if ip == nil {
				return nil, fmt.Errorf("invalid SSH bind address")
			}
			addresses[ip.String()] = true
		}
		if cfg.Port < 0 || cfg.Port > 65535 {
			return nil, fmt.Errorf("invalid SSH port")
		}
		if cfg.Port != 0 {
			ports[cfg.Port] = true
		}
	}
	// Bound both the cross-product and sshd's per-connection config work.
	if len(addresses) > 128 || len(ports) > 128 || (len(addresses)+1)*(len(ports)+1) > 1024 {
		return nil, fmt.Errorf("managed SSH endpoint rules exceed limit")
	}
	ips := make([]string, 0, len(addresses))
	for ip := range addresses {
		ips = append(ips, ip)
	}
	sort.Strings(ips)
	ps := make([]int, 0, len(ports))
	for port := range ports {
		ps = append(ps, port)
	}
	sort.Ints(ps)
	var output strings.Builder
	output.WriteString("# Managed by Sysbox: key lookup only.\n")
	write := func(match, args string) {
		fmt.Fprintf(&output, "Match %s\n  AuthorizedKeysCommand %s --ssh-authorized-keys %%u%s\n  AuthorizedKeysCommandUser %s\n", match, helper, args, commandUser)
	}
	for _, ip := range ips {
		for _, port := range ps {
			write(fmt.Sprintf("LocalAddress %s LocalPort %d", ip, port), fmt.Sprintf(" %s %d", ip, port))
		}
	}
	for _, ip := range ips {
		write("LocalAddress "+ip, " "+ip+" 0")
	}
	for _, port := range ps {
		write(fmt.Sprintf("LocalPort %d", port), fmt.Sprintf(" - %d", port))
	}
	write("all", "")
	return []byte(output.String()), nil
}
