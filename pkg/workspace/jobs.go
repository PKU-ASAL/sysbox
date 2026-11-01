package workspace

import (
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
)

func RunRecord(r controlplane.Run) controlplane.Run {
	controlstore.NormalizeRunProductFields(&r)
	return r
}
