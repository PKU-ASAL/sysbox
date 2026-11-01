package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSSHAccessCreateRejectsBeforeEffects(t *testing.T) {
	_, err := (SSHAccessResourceHandler{}).Create(context.Background(), nil, nil)
	require.ErrorIs(t, err, ErrConvergenceUnsupported)
}
