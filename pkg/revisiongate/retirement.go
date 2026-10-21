package revisiongate

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const batchPrefix = "retirement-batch-"
const batchIndex = ".retirement-index.json"
const batchAnchor = ".retirement-index.required"

type retirementIndex struct {
	Proofs []string `json:"proofs"`
	Digest string   `json:"digest"`
}

func indexDigest(proofs []string) string {
	raw, _ := json.Marshal(proofs)
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}
func readRetirementIndex(catalog string) (retirementIndex, bool, error) {
	var index retirementIndex
	anchor, anchorErr := os.Lstat(filepath.Join(catalog, batchAnchor))
	if anchorErr != nil && !os.IsNotExist(anchorErr) {
		return index, false, anchorErr
	}
	if anchorErr == nil && !anchor.Mode().IsRegular() {
		return index, false, fmt.Errorf("unsafe retirement anchor")
	}
	path := filepath.Join(catalog, batchIndex)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) && os.IsNotExist(anchorErr) {
		return index, false, nil
	}
	if err != nil {
		return index, false, fmt.Errorf("retirement index unavailable: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > 2<<20 {
		return index, false, fmt.Errorf("unsafe retirement index")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return index, false, err
	}
	if json.Unmarshal(raw, &index) != nil || index.Proofs == nil || len(index.Proofs) > 10000 || index.Digest != indexDigest(index.Proofs) {
		return index, false, fmt.Errorf("invalid retirement index")
	}
	if len(index.Proofs) > 0 && anchorErr != nil {
		return index, false, fmt.Errorf("retirement anchor missing")
	}
	seen := map[string]bool{}
	for _, name := range index.Proofs {
		if filepath.Base(name) != name || !IsBatchProof(name) || seen[name] {
			return index, false, fmt.Errorf("invalid retirement proof index")
		}
		seen[name] = true
	}
	return index, true, nil
}
func IsRetirementMetadata(name string) bool {
	return name == batchIndex || name == batchAnchor || IsBatchProof(name)
}

func IsBatchProof(name string) bool {
	return strings.HasPrefix(name, batchPrefix) && strings.HasSuffix(name, ".json")
}

// Retired includes single-object tombstones and immutable atomic batch proofs.
// Readers need no lock: the index appears by one rename, covering the whole batch.
func Retired(catalog, id string) (bool, error) {
	if _, err := os.Lstat(filepath.Join(catalog, id+".json.retired")); err == nil {
		return true, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	index, exists, err := readRetirementIndex(catalog)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, nil
	}
	retired := false
	for _, name := range index.Proofs {
		info, err := os.Lstat(filepath.Join(catalog, name))
		if err != nil {
			return false, err
		}
		if !info.Mode().IsRegular() || info.Size() > 16384 {
			return false, fmt.Errorf("unsafe retirement proof")
		}
		raw, err := os.ReadFile(filepath.Join(catalog, name))
		if err != nil {
			return false, err
		}
		if name != fmt.Sprintf("%s%x.json", batchPrefix, sha256.Sum256(raw)) {
			return false, fmt.Errorf("retirement proof digest mismatch")
		}
		var ids []string
		if json.Unmarshal(raw, &ids) != nil || len(ids) < 1 || len(ids) > 128 {
			return false, fmt.Errorf("invalid retirement proof")
		}
		seen := map[string]bool{}
		for _, candidate := range ids {
			if digest.FindString(candidate) != candidate || len(candidate) != 71 || seen[candidate] {
				return false, fmt.Errorf("invalid retired revision")
			}
			seen[candidate] = true
			if candidate == id {
				retired = true
			}
		}
	}
	return retired, nil
}

// RetireBatch requires the caller to hold the catalog lock and validate every
// reference and policy first. Index publication is the single logical commit;
// subsequent physical cleanup cannot make a subset visible again.
func RetireBatch(catalog string, ids []string) error {
	if len(ids) < 1 || len(ids) > 128 {
		return fmt.Errorf("invalid retirement batch")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if digest.FindString(id) != id || len(id) != 71 || seen[id] {
			return fmt.Errorf("invalid retirement identity")
		}
		seen[id] = true
		retired, err := Retired(catalog, id)
		if err != nil {
			return err
		}
		if retired {
			return fmt.Errorf("already retired")
		}
	}
	index, exists, err := readRetirementIndex(catalog)
	if err != nil {
		return err
	}
	if !exists {
		index.Proofs = []string{}
		index.Digest = indexDigest(index.Proofs)
		if err = writeRegistryRecord(filepath.Join(catalog, batchIndex), index); err != nil {
			return err
		}
	}
	if len(index.Proofs) >= 10000 {
		return fmt.Errorf("retirement index budget exceeded")
	}
	if err = writeRegistryRecord(filepath.Join(catalog, batchAnchor), map[string]int{"version": 1}); err != nil {
		return err
	}
	raw, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	name := fmt.Sprintf("%s%x.json", batchPrefix, sha256.Sum256(raw))
	if err = writeRegistryRecord(filepath.Join(catalog, name), ids); err != nil {
		return err
	}
	index.Proofs = append(index.Proofs, name)
	index.Digest = indexDigest(index.Proofs)
	return writeRegistryRecord(filepath.Join(catalog, batchIndex), index)
}
