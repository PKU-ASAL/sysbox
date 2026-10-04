package api

import (
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

func mustDecodeEvent(raw []byte) *controlplane.AgentCommandEvent {
	e, _ := persistence.DecodeCommandEvent(raw)
	return e
}
