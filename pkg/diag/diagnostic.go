package diag

import "github.com/oslab/sysbox/pkg/address"

type Severity string

const (
	Error   Severity = "error"
	Warning Severity = "warning"
)

type SourcePos struct {
	Line   int `json:"line"`
	Column int `json:"column"`
	Byte   int `json:"byte"`
}

type SourceRange struct {
	Filename string    `json:"filename"`
	Start    SourcePos `json:"start"`
	End      SourcePos `json:"end"`
}

type Diagnostic struct {
	Severity Severity         `json:"severity"`
	Summary  string           `json:"summary"`
	Detail   string           `json:"detail,omitempty"`
	Subject  *SourceRange     `json:"subject,omitempty"`
	Address  *address.Address `json:"address,omitempty"`
}
