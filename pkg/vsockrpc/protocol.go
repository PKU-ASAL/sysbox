// Package vsockrpc defines the wire protocol shared between sysbox-init
// (vsock server, running as PID 1 inside Firecracker microVMs) and the
// host-side VsockConnection that implements the provisioner Connection
// interface.
//
// Wire format (line-delimited JSON, one op per connection):
//
//	Client → Server   one Request JSON line, then op-specific body bytes
//	Server → Client   one or more Frame JSON lines, terminated by a frame
//	                  with Done=true
//
// Streams within a Frame carry payloads as base64 strings so the protocol
// stays line-delimited and easy to debug.
package vsockrpc

// VMConfig is the JSON schema written onto the per-VM config drive and
// read by sysbox-init inside the guest. MUST be the single source of truth;
// both pkg/provider/firecracker and cmd/sysbox-init reference this type.
type VMConfig struct {
	Hostname       string            `json:"hostname,omitempty"`
	AuthorizedKeys []string          `json:"authorized_keys,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	VsockPort      uint32            `json:"vsock_port,omitempty"`
	ChainInit      string            `json:"chain_init,omitempty"`
}

// DefaultPort is the vsock port the sysbox-init server listens on.
const DefaultPort uint32 = 8901

// Op enumerates supported operations.
type Op string

const (
	OpPing      Op = "ping"
	OpExec      Op = "exec"
	OpConsole   Op = "console"
	OpWriteFile Op = "write_file"
)

// Request is the single header sent by the client at the start of a connection.
type Request struct {
	Op   Op                `json:"op"`
	Cmd  []string          `json:"cmd,omitempty"`  // exec
	Env  map[string]string `json:"env,omitempty"`  // exec
	TTY  bool              `json:"tty,omitempty"`  // console
	Cols int               `json:"cols,omitempty"` // console
	Rows int               `json:"rows,omitempty"` // console
	Path string            `json:"path,omitempty"` // write_file / read_file
	Mode uint32            `json:"mode,omitempty"` // write_file (file mode bits)
	Size int64             `json:"size,omitempty"` // write_file (body bytes following the header)
}

// Frame is a server-to-client message. Exactly one of Stdout / Stderr /
// Pong / Header / Done will be non-empty per frame; Done=true marks the
// last frame and may carry ExitCode / Error.
type Frame struct {
	Stdout   []byte `json:"stdout,omitempty"`    // base64-encoded by the JSON encoder
	Stderr   []byte `json:"stderr,omitempty"`    // base64-encoded by the JSON encoder
	Pong     bool   `json:"pong,omitempty"`      // ping reply
	Done     bool   `json:"done,omitempty"`      // terminal frame
	ExitCode int    `json:"exit_code,omitempty"` // exec: command exit code
	Error    string `json:"error,omitempty"`     // any op: error message
}
