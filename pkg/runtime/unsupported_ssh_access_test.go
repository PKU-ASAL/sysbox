package runtime

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSSHAccessCreateRejectsBeforeEffects(t *testing.T) {
	_, err := (SSHAccessResourceHandler{}).Create(context.Background(), nil, nil)
	require.ErrorIs(t, err, ErrConvergenceUnsupported)
}
