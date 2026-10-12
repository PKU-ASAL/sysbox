package sshaccess

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestServiceProfileRejectsInvalidEnrollment(t *testing.T) {
	for _, scenario := range []string{"valid", "version", "unit", "port", "executable", "trailing", "permissions"} {
		t.Run(scenario, func(t *testing.T) {
			d, _, _ := deploymentFixture(t)
			p := ServiceProfile{Version: 1, Root: d.Root, ConfigPath: d.ConfigPath, DropInPath: d.DropInPath, Helper: "/usr/bin/true", CommandUser: "root", SSHD: "/usr/bin/true", Systemctl: "/usr/bin/true", ServiceUnit: "ssh.service", Probes: d.Probes, Listeners: []string{"127.0.0.1:22"}}
			switch scenario {
			case "version":
				p.Version = 2
			case "unit":
				p.ServiceUnit = "--help"
			case "port":
				p.Listeners = []string{"127.0.0.1:not-a-port"}
			case "executable":
				p.SSHD = "sshd"
			}
			raw, err := json.Marshal(p)
			require.NoError(t, err)
			if scenario == "trailing" {
				raw = append(raw, []byte(" {}")...)
			}
			path := filepath.Join(filepath.Dir(d.ConfigPath), "profile.json")
			require.NoError(t, os.WriteFile(path, raw, 0600))
			if scenario == "permissions" {
				require.NoError(t, os.Chmod(path, 0666))
			}
			_, err = LoadService(path)
			if scenario == "valid" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
