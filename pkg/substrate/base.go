package substrate

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hashicorp/hcl/v2"
)

// ValidationError marks an error as a plan-time validation failure. Callers
// (typically `sysbox plan`) can type-assert to distinguish "user spec is
// wrong" from "infra is broken".
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

// NewValidationError wraps a message in *ValidationError. Substrates should
// use this from Validate() so the runtime can present user-friendly errors.
func NewValidationError(format string, args ...any) error {
	return &ValidationError{Msg: fmt.Sprintf(format, args...)}
}

// IsValidationError reports whether err (or anything wrapped inside) is a
// *ValidationError. Convenience helper for plan-time error classification.
func IsValidationError(err error) bool {
	var v *ValidationError
	return errors.As(err, &v)
}

// BaseSubstrate provides safe default implementations for the optional
// interface methods so concrete substrates only have to implement the
// behaviour they actually support.
//
// Usage:
//
//	type Substrate struct {
//	    substrate.BaseSubstrate  // inherits Validate, DecodeProviderConfig defaults
//	    // ...substrate-specific fields...
//	}
//
// A concrete substrate overrides any method it wants to customise; the rest
// fall through to BaseSubstrate. Methods with no sensible default (Name,
// Capabilities, CreateNode, ...) are intentionally absent so the Go compiler
// flags missing implementations.
type BaseSubstrate struct{}

// Validate accepts any NodeSpec by default. Override to reject specs the
// substrate cannot honour (e.g. a docker substrate rejecting Kernel != "").
func (BaseSubstrate) Validate(NodeSpec) error { return nil }

// DecodeProviderConfig returns (nil, nil) by default: the substrate has no
// substrate-specific HCL fields. Override when a `provider "X" {}` block is
// declared in the schema.
func (BaseSubstrate) DecodeProviderConfig(hcl.Body, *hcl.EvalContext) (any, error) {
	return nil, nil
}

// Dependencies returns an empty ProviderDeps by default. Override when the
// substrate's typed Config references kernels/images/networks that runtime
// must apply first.
func (BaseSubstrate) Dependencies(any) ProviderDeps { return ProviderDeps{} }

// Connection returns nil by default. Substrates that provide a control-plane
// channel (docker-exec, vsock, SSH, WinRM) must override this.
func (BaseSubstrate) Connection(NodeHandle, []ConnectionHint) (Connection, error) {
	return nil, nil
}

// MarshalProviderState returns (nil, nil) by default: the substrate persists
// nothing beyond the NodeHandle.ID. Override when there is substrate-specific
// state to preserve across CLI invocations.
func (BaseSubstrate) MarshalProviderState(NodeHandle) (json.RawMessage, error) {
	return nil, nil
}

// UnmarshalProviderState returns (nil, nil) by default. Override in tandem
// with MarshalProviderState.
func (BaseSubstrate) UnmarshalProviderState(json.RawMessage) (any, error) {
	return nil, nil
}
