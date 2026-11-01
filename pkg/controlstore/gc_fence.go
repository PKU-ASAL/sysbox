package controlstore

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
)

func GcEvidenceHash(v any) string {
	raw, _ := json.Marshal(v)
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}
