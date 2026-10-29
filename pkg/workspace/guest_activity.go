package workspace

import (
	"fmt"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

func GuestActivityBeforeReceipt(activities []persistence.GuestActivity, receipt *controlplane.CleanupReceipt) error {
	for _, activity := range activities {
		if activity.Active() || activity.CreatedAt.IsZero() || activity.EndedAt.IsZero() || !activity.CreatedAt.Before(receipt.QueuedAt) || !activity.EndedAt.Before(receipt.QueuedAt) {
			return fmt.Errorf("%w: guest operation invalidates cleanup receipt", persistence.ErrConflict)
		}
	}
	return nil
}
