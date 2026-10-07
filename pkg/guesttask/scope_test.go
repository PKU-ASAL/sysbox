package guesttask

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGuestScopeRequiresExplicitVersionTwo(t *testing.T) {
	e, _, grant, _, _ := installFixture(t)
	root := filepath.Join(e.Root, ".scopes")
	require.NoError(t, os.MkdirAll(root, 0700))
	name := fmt.Sprintf("scope-%x.json", sha256.Sum256([]byte(grant.Grant.Scope.TopologyUID+"\x00"+grant.Grant.Scope.ResourceUID)))
	require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte(`{"epoch":1,"session":"session"}`), 0600))
	_, _, release, err := e.lockScope(context.Background(), grant.Grant)
	if release != nil {
		release()
	}
	require.ErrorContains(t, err, "rebuild")
}
