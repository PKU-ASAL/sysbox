package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWinRMConfigRejectsPlaintextAndUnsafeTrust(t *testing.T) {
	c := ConnectionConfig{Type: "winrm", Host: "https://192.0.2.10:5986/wsman", User: "operator", Password: "plain"}
	require.Error(t, c.ValidateCredentialReferences())
	c.Password = "secret://input/winrm_password"
	require.NoError(t, c.ValidateCredentialReferences())
	c.InsecureSkipHostKeyCheck = true
	require.Error(t, c.ValidateCredentialReferences())
}
func TestWinRMCredentialsRejectedBeforeDesiredState(t *testing.T) {
	root, err := ParseString(`resource "sysbox_node" "win" {
 image="sysbox_image.windows"
 substrate="substrate.libvirt.windows"
 connection {
 type="winrm"
 host="https://192.0.2.10:5986/wsman"
 user="operator"
 password="plain-secret-canary"
 }
 }`, "test.hcl")
	require.NoError(t, err)
	var node NodeConfig
	err = DecodeResource(&root.Resources[0], &node, nil)
	require.ErrorContains(t, err, "secret reference")
	require.NotContains(t, err.Error(), "plain-secret-canary")
}
