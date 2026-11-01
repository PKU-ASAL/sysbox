package controlplane

import (
	"context"
	"fmt"
	"time"
)

// Cleanup evidence uses the real host clock. Wait for a modest API/host skew
// before effects rather than changing timestamps or loosening receipt checks.
func WaitForCanonicalRunStart(ctx context.Context, started time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if started.IsZero() {
		return fmt.Errorf("canonical Run start time missing")
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		delay := time.Until(started)
		if delay <= 0 {
			return nil
		}
		if delay > 5*time.Second {
			return fmt.Errorf("host clock behind canonical Run start")
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-deadline.C:
			timer.Stop()
			return fmt.Errorf("host clock did not reach canonical Run start")
		case <-timer.C:
		}
	}
}
