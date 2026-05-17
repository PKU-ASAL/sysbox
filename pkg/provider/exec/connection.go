// Package exec provides concrete Connection implementations for provisioner
// execution. The Connection interface itself lives in pkg/substrate so
// substrates can implement it without import cycles; this package provides
// the docker-exec, SSH, and vsock implementations.
package exec

import (
	"github.com/oslab/sysbox/pkg/substrate"
)

// Connection is an alias for substrate.Connection for backward compatibility
// with callers that imported providerexec.Connection.
type Connection = substrate.Connection

// ConnectionHint is an alias for substrate.ConnectionHint.
type ConnectionHint = substrate.ConnectionHint
