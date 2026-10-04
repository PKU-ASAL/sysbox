package persistence

import (
	"bytes"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/controlplane"
)

func EncodeNodeOperation(x controlplane.NodeOperation) ([]byte, error) {
	if x.ID == "" || x.AgentID == "" || x.Topology == "" || x.Status == "" {
		return nil, ErrInvalidRecord
	}
	return json.Marshal(x)
}
func DecodeNodeOperation(raw []byte) (*controlplane.NodeOperation, error) {
	var x controlplane.NodeOperation
	if err := decodeGuestObject(raw, &x); err != nil {
		return nil, err
	}
	if x.ID == "" || x.AgentID == "" || x.Topology == "" || x.Status == "" {
		return nil, ErrCorruptRecord
	}
	return &x, nil
}
func SameNodeOperation(a, b controlplane.NodeOperation) bool {
	aa, _ := EncodeNodeOperation(a)
	bb, _ := EncodeNodeOperation(b)
	return bytes.Equal(aa, bb)
}
