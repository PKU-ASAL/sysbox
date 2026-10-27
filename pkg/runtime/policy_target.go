package runtime

import (
	"encoding/json"
	"fmt"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/state"
)

func routerPolicyTarget(current state.Resource) (driver.PolicyTarget, error) {
	target, err := driver.DecodePolicyTarget(json.RawMessage(current.Str("policy_target_state")))
	if err == nil && target.Resource != current.Address.String() {
		err = fmt.Errorf("router policy target address mismatch")
	}
	return target, err
}
