package libvirt

import (
	"context"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestUnownedLibvirtAdoptionBindsMetadataAndPreservesExternalDisk(t *testing.T) {
	root := t.TempDir()
	disk := filepath.Join(root, "foreign.qcow2")
	require.NoError(t, os.WriteFile(disk, []byte("foreign"), 0600))
	xmlPath := filepath.Join(root, "domain.xml")
	uuid := "11111111-2222-4333-8444-555555555555"
	xml, err := GenerateDomainXML(DomainSpec{Name: "foreign", UUID: uuid, DiskPath: disk})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(xmlPath, []byte(xml), 0600))
	t.Setenv("ADOPT_XML", xmlPath)
	virshFixture(t, `#!/usr/bin/env python3
import os,sys,xml.etree.ElementTree as E
p=os.environ['ADOPT_XML']; args=sys.argv[1:]
if args[0]=='dumpxml':print(open(p).read())
elif args[0]=='list':
 if os.path.exists(p):print(E.parse(p).getroot().findtext('uuid'))
elif args[0]=='undefine':os.unlink(p)
elif args[0]=='domstate':print('shut off')
elif args[0]=='metadata':
 root=E.parse(p).getroot(); metadata=root.find('metadata')
 if metadata is None:metadata=E.SubElement(root,'metadata')
 metadata.append(E.fromstring(args[args.index('--set')+1]));E.ElementTree(root).write(p,encoding='unicode')
 if os.environ.get('ADOPT_LOST_REPLY')=='1':sys.exit(11)
else:sys.exit(10)
`)
	s := New(root)
	id := substrate.OperationIdentity{ActionID: "adopt", OwnerUID: "owner", TopologyUID: "owner", ResourceUID: "resource", Generation: 1}
	ctx := substrate.WithOperationIdentity(context.Background(), id)
	evidence, err := s.InspectAdoption(ctx, uuid, substrate.NodeSpec{})
	require.NoError(t, err)
	require.NotNil(t, evidence.State)
	before, err := os.ReadFile(xmlPath)
	require.NoError(t, err)
	_, err = s.ObserveAdoption(ctx, evidence)
	require.Error(t, err)
	unchanged, err := os.ReadFile(xmlPath)
	require.NoError(t, err)
	require.Equal(t, before, unchanged)
	t.Setenv("ADOPT_LOST_REPLY", "1")
	_, err = s.ApplyAdoption(ctx, evidence)
	require.Error(t, err, "metadata applied but response lost")
	require.FileExists(t, disk)
	cold := New(root)
	h, err := cold.ObserveAdoption(ctx, evidence)
	require.NoError(t, err)
	other := id
	other.OwnerUID = "foreign"
	other.TopologyUID = "foreign"
	_, err = cold.ApplyAdoption(substrate.WithOperationIdentity(context.Background(), other), evidence)
	require.Error(t, err)

	changedAction := id
	changedAction.ActionID = "another-adopt"
	_, err = cold.ObserveAdoption(substrate.WithOperationIdentity(context.Background(), changedAction), evidence)
	require.Error(t, err)
	require.NoError(t, cold.DestroyNode(ctx, h))
	data, err := os.ReadFile(disk)
	require.NoError(t, err)
	require.Equal(t, []byte("foreign"), data)
}

func TestUnownedLibvirtRejectsChangedInactiveDefinition(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "domain.xml")
	external := "11111111-2222-4333-8444-555555555555"
	raw, err := GenerateDomainXML(DomainSpec{Name: "foreign", UUID: external})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte(raw), 0600))
	t.Setenv("ADOPT_XML", path)
	virshFixture(t, `#!/usr/bin/env python3
import os,sys
raw=open(os.environ['ADOPT_XML']).read()
if sys.argv[1]=='domstate':print('shut off')
elif sys.argv[1]=='dumpxml':
 if '--inactive' in sys.argv:raw=raw.replace('</domain>','<description>changed</description></domain>')
 print(raw)
else:sys.exit(17)
`)
	_, err = New(root).InspectAdoption(context.Background(), external, substrate.NodeSpec{})
	require.ErrorContains(t, err, "changed since inspection")
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, []byte(raw), after)
}
