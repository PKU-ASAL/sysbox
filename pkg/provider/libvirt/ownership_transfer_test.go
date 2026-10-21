package libvirt

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestOwnershipTransferPreservesExternalDomainAndFencesOldOwner(t *testing.T) {
	root := t.TempDir()
	old := managedOperation()
	old.TopologyUID = old.OwnerUID
	next := substrate.OperationIdentity{ActionID: "transfer", OwnerUID: "target", TopologyUID: "target", ResourceUID: "new-resource", Generation: old.Generation + 1}
	id := "11111111-2222-4333-8444-555555555555"
	hs := &HandleState{Identity: &old, DomainName: "domain", DomainUUID: id, ExternalDefinition: true, AdoptionFingerprint: "sha256:" + strings.Repeat("a", 64), NetworkInit: substrate.GuestNetworkInitPreconfigured}
	raw, err := GenerateDomainXML(DomainSpec{Name: "domain", UUID: id, VCPUs: 1, MemoryMiB: 128, Identity: &old, ManagedState: hs})
	require.NoError(t, err)
	path := filepath.Join(root, "domain.xml")
	require.NoError(t, os.WriteFile(path, []byte(raw), 0600))
	t.Setenv("TRANSFER_XML", path)
	helper := filepath.Join(root, "metadata.json")
	t.Setenv("TRANSFER_META", helper)
	virshFixture(t, `#!/bin/sh
case "$1" in
 domstate) echo 'shut off';;
 dumpxml) cat "$TRANSFER_XML";;
 metadata) shift; while [ "$1" != '--set' ]; do shift; done; shift; printf '%s' "$1" > "$TRANSFER_META";;
 *) exit 8;;
esac
`)
	plan := controlplane.OwnershipTransferPlan{Version: 1, ID: "transfer", SourceTopology: "source", TargetTopology: "target", SourceAddress: "sysbox_node.old", TargetAddress: "sysbox_node.new", Source: old, Target: next, AgentID: "host", Provider: "libvirt", ExternalID: id, Fingerprint: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(raw)))}
	digest, err := plan.Digest()
	require.NoError(t, err)
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	proof, err := controlplane.SignOwnershipRetirement(key, controlplane.OwnershipRetirementProof{Plan: plan, PlanDigest: digest, SourceEpoch: 1, SourceStateDigest: strings.Repeat("b", 64), RetiredAt: time.Now().UTC()})
	require.NoError(t, err)
	// A fake virsh readback cannot claim success before the metadata really changes.
	s := New(root)
	ctx := substrate.WithOperationIdentity(context.Background(), next)
	_, err = s.ApplyOwnershipTransfer(ctx, proof, pub)
	require.Error(t, err)
	encoded, err := os.ReadFile(helper)
	require.NoError(t, err)
	var metadata struct {
		XMLName xml.Name
		domManaged
	}
	require.NoError(t, xml.Unmarshal(encoded, &metadata))
	require.Equal(t, next.OwnerUID, metadata.OwnerUID)
	var transferred HandleState
	require.NoError(t, json.Unmarshal([]byte(metadata.State), &transferred))
	require.True(t, transferred.ExternalDefinition)
	require.Empty(t, transferred.DiskPath)
	updated, err := GenerateDomainXML(DomainSpec{Name: "domain", UUID: id, VCPUs: 1, MemoryMiB: 128, Identity: &next, ManagedState: &transferred})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte(updated), 0600))
	handle, err := s.ApplyOwnershipTransfer(ctx, proof, pub)
	require.NoError(t, err)
	require.Equal(t, id, handle.ID)
	require.Equal(t, next.Generation, handle.Generation)
	_, err = s.ApplyOwnershipTransfer(substrate.WithOperationIdentity(context.Background(), old), proof, pub)
	require.Error(t, err)
}

func TestOwnershipPreparationRejectsCorruptRecoveryState(t *testing.T) {
	id := "11111111-2222-4333-8444-555555555555"
	old := managedOperation()
	old.TopologyUID = old.OwnerUID
	raw, err := GenerateDomainXML(DomainSpec{Name: "domain", UUID: id, VCPUs: 1, MemoryMiB: 128, Identity: &old, ManagedState: &HandleState{Identity: &old, DomainName: "wrong-name", DomainUUID: id}})
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "domain.xml")
	require.NoError(t, os.WriteFile(path, []byte(raw), 0600))
	t.Setenv("TRANSFER_XML", path)
	virshFixture(t, `#!/bin/sh
case "$1" in dumpxml) cat "$TRANSFER_XML";; domstate) echo 'shut off';; *) exit 9;; esac
`)
	p := controlplane.OwnershipTransferPlan{Version: 1, ID: "transfer", SourceTopology: "source", TargetTopology: "target", SourceAddress: "sysbox_node.old", TargetAddress: "sysbox_node.new", AgentID: "host", Provider: "libvirt", ExternalID: id, Source: old, Target: substrate.OperationIdentity{ActionID: "transfer", OwnerUID: "target", TopologyUID: "target", ResourceUID: "new", Generation: old.Generation + 1}, Fingerprint: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(raw)))}
	require.Error(t, New(t.TempDir()).InspectOwnershipTransfer(context.Background(), p))
}
