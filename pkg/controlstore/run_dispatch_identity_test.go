package controlstore_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/controlstore"
)

func TestDispatchIdentityConflict(t *testing.T) {
	const request = "apply-request:abc"
	const dispatch = "run-dispatch:run-1:apply:host-a"

	for name, testCase := range map[string]struct {
		current, requested string
		conflict           bool
	}{
		"an unbound run accepts any identity":            {current: "", requested: request, conflict: false},
		"the same identity replays":                      {current: request, requested: request, conflict: false},
		"a request-bound run accepts its command marker": {current: request, requested: dispatch, conflict: false},
		"a different request conflicts":                  {current: request, requested: "apply-request:other", conflict: true},
		"a different command marker conflicts":           {current: dispatch, requested: "run-dispatch:run-2:apply:host-a", conflict: true},
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, testCase.conflict, controlstore.DispatchIdentityConflict(testCase.current, testCase.requested))
		})
	}
}
