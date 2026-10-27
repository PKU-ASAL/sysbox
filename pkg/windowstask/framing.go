package windowstask

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

const MaxCallSize = 32 << 20

func ReadCall(reader io.Reader) (Call, error) {
	var call Call
	raw, err := io.ReadAll(io.LimitReader(reader, MaxCallSize+1))
	if err != nil {
		return call, err
	}
	if len(raw) > MaxCallSize {
		return call, fmt.Errorf("Windows task call too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&call); err != nil {
		return call, err
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return call, fmt.Errorf("one task call required")
	}
	return call, nil
}
