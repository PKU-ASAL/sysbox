package diag

import "github.com/oslab/sysbox/pkg/address"

type Severity string

const (
	Error   Severity = "error"
	Warning Severity = "warning"
)

type SourcePos struct {
	Line   int
	Column int
	Byte   int
}

type SourceRange struct {
	Filename string
	Start    SourcePos
	End      SourcePos
}

type Diagnostic struct {
	Severity Severity
	Summary  string
	Detail   string
	Subject  *SourceRange
	Address  *address.Address
}
