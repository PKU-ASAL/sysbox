package libvirt

import (
	"context"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestGCRequiresIndependentDomainAbsence(t *testing.T) {
	for _, mode := range []string{"absent", "present", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, "deleted")
			t.Setenv("GC_MARKER", marker)
			t.Setenv("GC_MODE", mode)
			id := managedOperation()
			id.TopologyUID = id.OwnerUID
			uuid := "11111111-2222-4333-8444-555555555555"
			xml, err := GenerateDomainXML(DomainSpec{Name: "gc", UUID: uuid, Identity: &id, ManagedState: &HandleState{Identity: &id, DomainName: "gc", DomainUUID: uuid}})
			require.NoError(t, err)
			file := filepath.Join(dir, "domain.xml")
			require.NoError(t, os.WriteFile(file, []byte(xml), 0600))
			t.Setenv("GC_XML", file)
			virshFixture(t, `#!/bin/sh
case "$1" in
list)
 if [ -e "$GC_MARKER" ]; then
  case "$GC_MODE" in absent) exit 0;; unavailable) echo 'daemon unavailable' >&2; exit 2;; esac
 fi
 echo '11111111-2222-4333-8444-555555555555';;
dumpxml)
 if [ -e "$GC_MARKER" ]; then
  case "$GC_MODE" in absent) echo 'Domain not found' >&2; exit 1;; unavailable) echo 'daemon unavailable' >&2; exit 2;; esac
 fi
 cat "$GC_XML";;
domstate) echo 'shut off';;
undefine) touch "$GC_MARKER";;
*) exit 7;;
esac
`)
			err = New(dir).DeleteManagedExternal(context.Background(), uuid, id.OwnerUID, id.ResourceUID, id.Generation)
			if mode == "absent" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
