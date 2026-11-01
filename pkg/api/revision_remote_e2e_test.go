//go:build e2e

package api

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/revisiongate"
	"github.com/stretchr/testify/require"
)

func TestRemoteRegistryAcrossIsolatedVMHTTPS(t *testing.T) {
	root := os.Getenv("SYSBOX_REMOTE_REGISTRY_LAB")
	if root == "" {
		t.Skip("requires isolated lab")
	}
	var lab struct {
		Ports map[string]int `json:"ports"`
		ID    string         `json:"id"`
	}
	raw, err := os.ReadFile(filepath.Join(root, "lab.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &lab))
	var scope struct {
		Disposable bool   `json:"disposable"`
		LabID      string `json:"lab_id"`
	}
	raw, err = os.ReadFile(filepath.Join(root, "fencing-scope.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &scope))
	require.True(t, scope.Disposable)
	require.Equal(t, lab.ID, scope.LabID)
	round := filepath.Join(root, "remote-registry-"+uuid.NewString())
	require.NoError(t, os.Mkdir(round, 0700))
	upload := func(local, remote string) {
		t.Helper()
		out, err := exec.Command("scp", "-F", "/dev/null", "-q", "-i", filepath.Join(root, "client"), "-P", fmt.Sprint(lab.Ports["execution"]), "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile="+filepath.Join(root, "known_hosts"), local, "p9@127.0.0.1:"+remote).CombinedOutput()
		require.NoError(t, err, string(out))
	}
	ssh := func(command string) {
		t.Helper()
		out, err := exec.Command("ssh", "-F", "/dev/null", "-i", filepath.Join(root, "client"), "-p", fmt.Sprint(lab.Ports["execution"]), "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile="+filepath.Join(root, "known_hosts"), "p9@127.0.0.1", command).CombinedOutput()
		require.NoError(t, err, string(out))
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "isolated registry"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("10.0.2.2")}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	require.NoError(t, err)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	pair, err := tls.X509KeyPair(certPEM, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	require.NoError(t, err)
	certPath := filepath.Join(round, "ca.pem")
	require.NoError(t, os.WriteFile(certPath, certPEM, 0600))
	tokenPath := filepath.Join(round, "token")
	require.NoError(t, os.WriteFile(tokenPath, []byte(uuid.NewString()), 0600))
	cfg := config.DefaultServiceConfig()
	cfg.Paths.RunsDir = filepath.Join(round, "registry")
	cfg.Paths.WorkspacesDir = filepath.Join(round, "workspaces")
	cfg.RevisionConsumers = map[string]string{"remote-vm": tokenPath}
	server := NewServerWithConfig(cfg)
	require.NoError(t, server.initErr)
	httpServer := httptest.NewUnstartedServer(server)
	httpServer.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
	httpServer.StartTLS()
	defer httpServer.Close()
	files := map[string][]byte{"field.sysbox.hcl": []byte("separate VM")}
	id := controlplane.ComputeRevisionDigest(files)
	ctx := context.Background()
	store := server.apiStore.(*controlstore.Local)
	require.NoError(t, store.SaveGlobalRevision(ctx, controlplane.GlobalRevision{Revision: id, Files: files, CreatedAt: time.Now().Add(-time.Hour)}))
	remoteDir := "/var/tmp/rr-" + uuid.NewString()
	ssh("mkdir -m700 " + remoteDir)
	upload(certPath, remoteDir+"/ca.pem")
	upload(tokenPath, remoteDir+"/token")
	upload(os.Getenv("SYSBOX_REMOTE_REGISTRY_PROBE_BINARY"), remoteDir+"/probe.test")
	ssh("chmod 700 " + remoteDir + "/probe.test")
	binding := revisiongate.RemoteBinding{URL: strings.Replace(httpServer.URL, "127.0.0.1", "10.0.2.2", 1) + "/v1/revision-registry", Consumer: "remote-vm", TokenFile: remoteDir + "/token"}
	probe := func(mode string) {
		raw, err := json.Marshal(map[string]any{"Root": remoteDir + "/consumer", "ID": id, "Mode": mode, "Binding": binding})
		require.NoError(t, err)
		path := filepath.Join(round, "probe.json")
		require.NoError(t, os.WriteFile(path, raw, 0600))
		upload(path, remoteDir+"/input.json")
		ssh("SSL_CERT_FILE=" + remoteDir + "/ca.pem SYSBOX_REMOTE_REGISTRY_PROBE=" + remoteDir + "/input.json " + remoteDir + "/probe.test -test.run=^TestRemoteRegistryGuestProbe$ -test.v")
	}
	require.Error(t, store.DeleteGlobalRevisionAtomic(ctx, id, time.Now()))
	probe("enroll")
	probe("pin")
	require.Error(t, store.DeleteGlobalRevisionAtomic(ctx, id, time.Now()))
	probe("release")
	require.NoError(t, store.DeleteGlobalRevisionAtomic(ctx, id, time.Now()))
	probe("late")
	require.NoError(t, os.WriteFile(filepath.Join(round, "result.json"), []byte(`{"https":true,"separate_vm":true,"late_reference_rejected":true}`), 0600))
}
