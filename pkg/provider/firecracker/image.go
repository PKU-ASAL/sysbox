package firecracker

import "github.com/oslab/sysbox/pkg/substrate"

// Verify interface compliance.
var _ substrate.Substrate = (*Substrate)(nil)
