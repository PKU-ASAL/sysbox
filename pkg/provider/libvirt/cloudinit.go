package libvirt

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type noCloudNetwork struct {
	Version   int                        `yaml:"version"`
	Ethernets map[string]noCloudEthernet `yaml:"ethernets"`
}

type noCloudEthernet struct {
	Match     map[string]string `yaml:"match"`
	DHCP4     bool              `yaml:"dhcp4"`
	Addresses []string          `yaml:"addresses"`
	Routes    []noCloudRoute    `yaml:"routes,omitempty"`
}

type noCloudRoute struct {
	To  string `yaml:"to"`
	Via string `yaml:"via"`
}

type noCloudUserData struct {
	Users []noCloudUser `yaml:"users,omitempty"`
}

type noCloudUser struct {
	Name              string   `yaml:"name"`
	Shell             string   `yaml:"shell"`
	Sudo              string   `yaml:"sudo"`
	SSHAuthorizedKeys []string `yaml:"ssh_authorized_keys"`
}

func buildNoCloudNetworkConfig(bridges []BridgeAttach) ([]byte, error) {
	config := noCloudNetwork{Version: 2, Ethernets: map[string]noCloudEthernet{}}
	for i, bridge := range bridges {
		if bridge.MAC == "" || len(bridge.IPPrefixes) == 0 {
			continue
		}
		ethernet := noCloudEthernet{Match: map[string]string{"macaddress": bridge.MAC}, DHCP4: false, Addresses: append([]string(nil), bridge.IPPrefixes...)}
		if bridge.Gateway != "" {
			ethernet.Routes = []noCloudRoute{{To: "0.0.0.0/0", Via: bridge.Gateway}}
		}
		config.Ethernets[fmt.Sprintf("sysbox%d", i)] = ethernet
	}
	return yaml.Marshal(config)
}

func buildNoCloudUserData(user, authorizedKey string) ([]byte, error) {
	if authorizedKey == "" {
		return []byte("#cloud-config\n"), nil
	}
	data, err := yaml.Marshal(noCloudUserData{Users: []noCloudUser{{
		Name: user, Shell: "/bin/bash", Sudo: "ALL=(ALL) NOPASSWD:ALL", SSHAuthorizedKeys: []string{authorizedKey},
	}}})
	if err != nil {
		return nil, err
	}
	return append([]byte("#cloud-config\n"), data...), nil
}

func createNoCloudSeed(vmDir, name string, bridges []BridgeAttach, user, authorizedKey string) (string, error) {
	networkConfig, err := buildNoCloudNetworkConfig(bridges)
	if err != nil {
		return "", err
	}
	userData, err := buildNoCloudUserData(user, authorizedKey)
	if err != nil {
		return "", err
	}
	seedDir := filepath.Join(vmDir, "seed")
	if err := os.MkdirAll(seedDir, 0o755); err != nil {
		return "", err
	}
	files := map[string][]byte{
		"meta-data":      []byte("instance-id: " + name + "\nlocal-hostname: " + name + "\n"),
		"user-data":      userData,
		"network-config": networkConfig,
	}
	for filename, data := range files {
		if err := os.WriteFile(filepath.Join(seedDir, filename), data, 0o644); err != nil {
			return "", err
		}
	}
	seedISO := filepath.Join(vmDir, "seed.iso")
	cmd := exec.Command("genisoimage", "-quiet", "-output", seedISO, "-volid", "cidata", "-joliet", "-rock", "user-data", "meta-data", "network-config")
	cmd.Dir = seedDir
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("create NoCloud seed: %w (%s)", err, output)
	}
	if err := os.Chmod(seedISO, 0o644); err != nil {
		return "", err
	}
	return seedISO, nil
}
