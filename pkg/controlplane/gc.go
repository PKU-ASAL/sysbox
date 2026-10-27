package controlplane

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

type GCCandidate struct {
	Kind            string `json:"kind"`
	ExternalID      string `json:"external_id"`
	TopologyUID     string `json:"topology_uid"`
	OwnerUID        string `json:"owner_uid"`
	ResourceUID     string `json:"resource_uid"`
	Generation      int64  `json:"generation"`
	InventoryDigest string `json:"inventory_digest"`
}

func (c GCCandidate) Validate() error {
	if c.Kind != "container" && c.Kind != "network" && c.Kind != "domain" && c.Kind != "firecracker" {
		return fmt.Errorf("invalid GC candidate kind")
	}
	if c.ExternalID == "" || c.TopologyUID == "" || c.OwnerUID == "" || c.ResourceUID == "" || c.Generation < 1 || c.InventoryDigest == "" {
		return fmt.Errorf("incomplete GC candidate identity")
	}
	if c.TopologyUID != c.OwnerUID {
		return fmt.Errorf("GC owner and topology identity differ")
	}
	return nil
}
func (c GCCandidate) Digest() (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	raw, _ := json.Marshal(c)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
