package firecracker

import (
	"context"
	"fmt"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestFencedGCInspectionRejectsWrongManifestOwner(t *testing.T) {
	s, h, _, _ := manifestFixture(t, false)
	hs := h.Provider.(*HandleState)
	presence, err := s.InspectFencedGC(context.Background(), h.ID, hs.OwnerUID, hs.ResourceUID, hs.Generation)
	require.NoError(t, err)
	require.Equal(t, "present", presence)
	_, err = s.InspectFencedGC(context.Background(), h.ID, "foreign", hs.ResourceUID, hs.Generation)
	require.Error(t, err)
}

func TestFencedGCInspectionRejectsUnmanifestedScopedResidue(t *testing.T) {
	s, h, _, _ := manifestFixture(t, false)
	hs := h.Provider.(*HandleState)
	require.NoError(t, os.RemoveAll(hs.VMDir))
	require.NoError(t, os.MkdirAll(filepath.Join(s.rootfsDir, hs.OwnerUID, hs.ResourceUID, fmt.Sprint(hs.Generation)), 0700))
	_, err := s.InspectFencedGC(context.Background(), h.ID, hs.OwnerUID, hs.ResourceUID, hs.Generation)
	require.Error(t, err)
}
