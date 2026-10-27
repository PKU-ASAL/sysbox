package libvirt

import (
	"context"
	"fmt"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAttachmentDiscoveryRequiresOwnedDomain(t *testing.T) {
	discovery, ok := any(New(t.TempDir())).(driver.ManagedAttachmentDiscovery)
	require.True(t, ok)
	_, err := discovery.DiscoverManagedAttachments(context.Background(), substrate.NodeHandle{}, nil)
	require.Error(t, err)
}

func TestAttachmentDiscoveryRejectsUnaccountedDomainInterface(t *testing.T) {
	for _, extra := range []bool{false, true} {
		t.Run(fmt.Sprint(extra), func(t *testing.T) {
			id := managedOperation()
			hs := &HandleState{DomainName: "node", DomainUUID: "11111111-2222-4333-8444-555555555555", Identity: &id}
			raw, err := GenerateDomainXML(DomainSpec{Name: hs.DomainName, UUID: hs.DomainUUID, VCPUs: 1, MemoryMiB: 128, Identity: &id, ManagedState: hs})
			require.NoError(t, err)
			if extra {
				raw = strings.Replace(raw, "</devices>", `<interface type="bridge"><source bridge="untracked"/></interface></devices>`, 1)
			}
			path := filepath.Join(t.TempDir(), "domain.xml")
			require.NoError(t, os.WriteFile(path, []byte(raw), 0600))
			t.Setenv("SYSBOX_DISCOVERY_DOMAIN", path)
			virshFixture(t, "#!/bin/sh\ncase \"$1\" in\nlist) echo "+hs.DomainUUID+";;\ndumpxml) cat \"$SYSBOX_DISCOVERY_DOMAIN\";;\n*) exit 1;;\nesac\n")
			found, err := New(t.TempDir()).DiscoverManagedAttachments(substrate.WithOperationIdentity(context.Background(), id), managedNodeHandle(hs), nil)
			if extra {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Empty(t, found)
			}
		})
	}
}
