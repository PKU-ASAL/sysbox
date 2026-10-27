package firecracker

import (
	"context"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
)

// Guest policy remains unavailable until the guest protocol retains generation
// ownership, deletion tombstones and atomic replacement. The former raw nft
// adapter cannot safely participate in observation-driven recovery.
func (*Substrate) ApplyRuleset(context.Context, driver.PolicyTarget, driver.RulesetSpec) (driver.RulesetObservation, error) {
	return driver.RulesetObservation{}, substrate.ErrNotSupported
}
func (*Substrate) ObserveRuleset(context.Context, driver.PolicyTarget, string) (driver.RulesetObservation, error) {
	return driver.RulesetObservation{}, substrate.ErrNotSupported
}
func (*Substrate) DeleteRuleset(context.Context, driver.PolicyTarget, string) error {
	return substrate.ErrNotSupported
}

var _ driver.Policy = (*Substrate)(nil)
