package api

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestImportRejectsMissingOwnershipAdoptionBeforeDispatch(t *testing.T) {
	service := &NodeOperationService{}
	_, err := service.Import(context.Background(), "lab", ImportNodeRequest{Type: "sysbox_node", Name: "node", ID: "external", Substrate: "docker"}, requestSubject{})
	require.ErrorContains(t, err, "ownership adoption")
}
