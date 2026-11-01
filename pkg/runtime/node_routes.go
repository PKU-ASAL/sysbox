package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

// configureNodeRoutes is shared by create and reset. Both call it after guest
// networking is ready and before provisioners can depend on declared routes.
func (e *Executor) configureNodeRoutes(ctx context.Context, step int, id address.Address, driverName string, handle substrate.NodeHandle, routes []config.RouteConfig, target *state.Resource) error {
	if len(routes) == 0 {
		if target != nil && target.AttributeMap()["routes"] != nil {
			return target.SetAttribute("routes", []map[string]string{})
		}
		return nil
	}
	if target == nil {
		return fmt.Errorf("route setup for %s has no staged resource", id)
	}
	guestNetwork, err := e.providers.RequireGuestNetwork(driverName)
	if err != nil {
		return err
	}
	for _, route := range routes {
		e.logf("[route] %s: %s via %s\n", id.Name, route.Destination, route.Via)
		err := e.recordSubstep(step, "attach_route", map[string]any{"resource": id.String(), "dst": route.Destination, "via": route.Via}, func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			present, err := guestNetwork.HasRoute(ctx, handle, route.Destination, route.Via)
			if err != nil {
				return fmt.Errorf("%w: observe route %s: %w", ErrObservationUnknown, route.Destination, err)
			}
			if present {
				return nil
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			effectErr := guestNetwork.EnsureRoute(ctx, handle, route.Destination, route.Via)
			if err := ctx.Err(); err != nil {
				return errors.Join(err, effectErr)
			}
			present, err = guestNetwork.HasRoute(ctx, handle, route.Destination, route.Via)
			if err != nil {
				return errors.Join(fmt.Errorf("%w: verify route %s: %w", ErrObservationUnknown, route.Destination, err), effectErr)
			}
			if !present {
				return errors.Join(fmt.Errorf("%w: route %s via %s remains absent", ErrVerification, route.Destination, route.Via), effectErr)
			}
			// A lost EnsureRoute response does not require a repeated effect when
			// the point observation proves the requested route is already present.
			return nil
		})
		if err != nil {
			return err
		}
	}
	specifications := make([]map[string]string, 0, len(routes))
	for _, route := range routes {
		specifications = append(specifications, map[string]string{"dst": route.Destination, "via": route.Via})
	}
	return target.SetAttribute("routes", specifications)
}
