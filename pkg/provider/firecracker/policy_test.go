package firecracker

import (
	"context"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestGuestPolicyRejectsUnfencedLegacyAdapter(t *testing.T) {
	s := &Substrate{}
	_, err := s.ApplyRuleset(context.Background(), driver.PolicyTarget{}, driver.RulesetSpec{})
	require.ErrorIs(t, err, substrate.ErrNotSupported)
	_, err = s.ObserveRuleset(context.Background(), driver.PolicyTarget{}, "owner")
	require.ErrorIs(t, err, substrate.ErrNotSupported)
	require.ErrorIs(t, s.DeleteRuleset(context.Background(), driver.PolicyTarget{}, "owner"), substrate.ErrNotSupported)
}
