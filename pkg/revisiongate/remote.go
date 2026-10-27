package revisiongate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"io"
	"io/fs"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const remoteBindingFile = ".remote-registry.json"
const remoteConsumersFile = "remote-consumers.json"
const remoteProtocol = 1
const remoteConsumerAnchor = ".remote-consumers.required"
const remoteBindingAnchor = ".remote-binding.required"
const remoteRootAnchor = ".sysbox-remote-registry.required"

type RemoteBinding struct {
	Sequence    uint64 `json:"sequence"`
	catalog     string
	URL         string `json:"url"`
	Consumer    string `json:"consumer"`
	TokenFile   string `json:"token_file"`
	Incarnation string `json:"incarnation"`
	Version     int    `json:"version"`
}
type remoteConsumer struct {
	Sequence    uint64    `json:"sequence"`
	RequestHash string    `json:"request_hash"`
	ID          string    `json:"id"`
	KeyHash     string    `json:"key_hash"`
	Incarnation string    `json:"incarnation"`
	References  []string  `json:"references"`
	ValidUntil  time.Time `json:"valid_until"`
	Ready       bool      `json:"ready"`
}
type remoteUpdate struct {
	Sequence    uint64   `json:"sequence"`
	Version     int      `json:"version"`
	Incarnation string   `json:"incarnation"`
	References  []string `json:"references"`
	Replace     bool     `json:"replace"`
}
type RemoteRegistry struct {
	Root string
	keys map[string][]byte
}

func remoteName(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
func privateToken(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 4096 {
		return nil, fmt.Errorf("remote registry token must be private regular file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) < 8 {
		return nil, fmt.Errorf("remote registry token too short")
	}
	return raw, nil
}
func readRemoteConsumers(root string) (map[string]remoteConsumer, error) {
	path := filepath.Join(root, remoteConsumersFile)
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 16<<20 {
		return nil, fmt.Errorf("invalid remote consumer catalog")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var entries map[string]remoteConsumer
	if json.Unmarshal(raw, &entries) != nil || entries == nil || len(entries) > 1024 {
		return nil, fmt.Errorf("invalid remote consumers")
	}
	for id, entry := range entries {
		if id != entry.ID || !remoteName(id) || len(entry.KeyHash) != 64 || len(entry.References) > 10000 {
			return nil, fmt.Errorf("invalid remote consumer identity")
		}
		for _, ref := range entry.References {
			if len(ref) != 71 || digest.FindString(ref) != ref {
				return nil, fmt.Errorf("invalid remote reference")
			}
		}
	}
	return entries, nil
}

// Enrollment is monotonic: removing a configured consumer never drops its
// durable hold. Offline/uninitialized consumers block every retirement.
func NewRemoteRegistry(root string, keyFiles map[string]string) (*RemoteRegistry, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	unlock, err := Lock(context.Background(), root)
	if err != nil {
		return nil, err
	}
	defer unlock()
	entries, err := readRemoteConsumers(root)
	if os.IsNotExist(err) {
		if _, e := os.Lstat(filepath.Join(root, remoteConsumerAnchor)); !os.IsNotExist(e) {
			return nil, fmt.Errorf("remote enrollment evidence missing")
		}
		entries = map[string]remoteConsumer{}
	} else if err != nil {
		return nil, err
	}
	keys := map[string][]byte{}
	for id, path := range keyFiles {
		if !remoteName(id) {
			return nil, fmt.Errorf("invalid remote consumer")
		}
		key, err := privateToken(path)
		if err != nil {
			return nil, err
		}
		hash := sha256.Sum256(key)
		keyHash := hex.EncodeToString(hash[:])
		entry, exists := entries[id]
		if exists && entry.KeyHash != keyHash {
			return nil, fmt.Errorf("remote consumer key replacement requires explicit migration")
		}
		if !exists {
			entries[id] = remoteConsumer{ID: id, KeyHash: keyHash, References: []string{}}
		}
		keys[id] = key
	}
	if err = writeRegistryRecord(filepath.Join(root, remoteConsumersFile), entries); err != nil {
		return nil, err
	}
	if err = writeRegistryRecord(filepath.Join(root, remoteConsumerAnchor), map[string]int{"version": remoteProtocol}); err != nil {
		return nil, err
	}
	return &RemoteRegistry{Root: root, keys: keys}, nil
}
func (s *RemoteRegistry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if r.Method != "POST" || len(parts) != 2 || parts[1] != "pins" {
		http.Error(w, "not found", 404)
		return
	}
	key, ok := s.keys[parts[0]]
	provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || subtle.ConstantTimeCompare([]byte(provided), key) != 1 {
		http.Error(w, "unauthorized", 401)
		return
	}
	var update remoteUpdate
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&update) != nil || decoder.Decode(new(any)) != io.EOF || update.Version != remoteProtocol || update.Sequence == 0 || !remoteName(update.Incarnation) || len(update.References) > 10000 {
		http.Error(w, "invalid pins", 400)
		return
	}
	for _, ref := range update.References {
		if len(ref) != 71 || digest.FindString(ref) != ref {
			http.Error(w, "invalid reference", 400)
			return
		}
	}
	unlock, err := Lock(r.Context(), s.Root)
	if err != nil {
		http.Error(w, "catalog unavailable", 503)
		return
	}
	defer unlock()
	entries, err := readRemoteConsumers(s.Root)
	if err != nil {
		http.Error(w, "consumer evidence unavailable", 409)
		return
	}
	entry, ok := entries[parts[0]]
	hash := sha256.Sum256(key)
	if !ok || entry.KeyHash != hex.EncodeToString(hash[:]) || (entry.Incarnation != "" && entry.Incarnation != update.Incarnation) {
		http.Error(w, "consumer identity conflict", 409)
		return
	}
	updateRaw, _ := json.Marshal(update)
	updateSum := sha256.Sum256(updateRaw)
	updateHash := hex.EncodeToString(updateSum[:])
	if update.Sequence < entry.Sequence || (update.Sequence == entry.Sequence && entry.RequestHash != updateHash) {
		http.Error(w, "stale remote update", 409)
		return
	}
	if update.Sequence == entry.Sequence && entry.RequestHash == updateHash {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"accepted": true})
		return
	}
	refs := map[string]bool{}
	if !update.Replace {
		for _, ref := range entry.References {
			refs[ref] = true
		}
	}
	for _, ref := range update.References {
		retired, err := Retired(s.Root, ref)
		if err != nil || retired {
			http.Error(w, "reference retired or unavailable", 409)
			return
		}
		refs[ref] = true
	}
	if len(refs) > 10000 {
		http.Error(w, "reference budget exceeded", 409)
		return
	}
	entry.References = []string{}
	for ref := range refs {
		entry.References = append(entry.References, ref)
	}
	sort.Strings(entry.References)
	entry.Incarnation = update.Incarnation
	entry.Sequence = update.Sequence
	entry.RequestHash = updateHash
	// Only full local scans establish completeness or renew its lifetime.
	if update.Replace {
		entry.Ready = true
		entry.ValidUntil = time.Now().UTC().Add(time.Hour)
	}
	entries[entry.ID] = entry
	if err = writeRegistryRecord(filepath.Join(s.Root, remoteConsumersFile), entries); err != nil {
		http.Error(w, "publication failed", 503)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"accepted": true})
}

// Caller holds the registry catalog lock through deletion commit.
func ValidateRemoteConsumers(root, id string, now time.Time) error {
	entries, err := readRemoteConsumers(root)
	if os.IsNotExist(err) {
		if _, e := os.Lstat(filepath.Join(root, remoteConsumerAnchor)); !os.IsNotExist(e) {
			return fmt.Errorf("remote enrollment evidence missing")
		}
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.Ready || !entry.ValidUntil.After(now) {
			return fmt.Errorf("remote consumer %s offline or incomplete", entry.ID)
		}
		for _, ref := range entry.References {
			if ref == id {
				return fmt.Errorf("remote consumer %s retains revision", entry.ID)
			}
		}
	}
	return nil
}
func readRemoteBinding(root string) (RemoteBinding, bool, error) {
	var binding RemoteBinding
	path := filepath.Join(root, remoteBindingFile)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if _, e := os.Lstat(filepath.Join(root, remoteBindingAnchor)); !os.IsNotExist(e) {
			return binding, false, fmt.Errorf("remote binding missing")
		}
		return binding, false, nil
	}
	if err != nil {
		return binding, false, err
	}
	if !info.Mode().IsRegular() || info.Size() > 16384 {
		return binding, false, fmt.Errorf("invalid remote registry binding")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return binding, false, err
	}
	if json.Unmarshal(raw, &binding) != nil || binding.Version != remoteProtocol || !remoteName(binding.Consumer) || !remoteName(binding.Incarnation) {
		return binding, false, fmt.Errorf("invalid remote binding")
	}
	binding.catalog = root
	return binding, true, nil
}
func postRemote(ctx context.Context, binding RemoteBinding, refs []string, replace bool) error {
	// Caller holds the local catalog lock. Persist the request sequence before
	// sending so a timed-out earlier replace can never erase later pins.
	current, exists, err := readRemoteBinding(binding.catalog)
	if err != nil {
		return err
	}
	if !exists || current.Sequence == math.MaxUint64 {
		return fmt.Errorf("remote binding sequence unavailable")
	}
	current.Sequence++
	binding = current
	if err = writeRegistryRecord(filepath.Join(binding.catalog, remoteBindingFile), binding); err != nil {
		return err
	}
	key, err := privateToken(binding.TokenFile)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(remoteUpdate{Sequence: binding.Sequence, Version: remoteProtocol, Incarnation: binding.Incarnation, References: refs, Replace: replace})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(binding.URL, "/")+"/"+binding.Consumer+"/pins", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+string(key))
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("remote registry redirects forbidden") }}
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("remote registry admission rejected (%d)", response.StatusCode)
	}
	var result struct {
		Accepted bool `json:"accepted"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&result) != nil || !result.Accepted {
		return fmt.Errorf("remote registry acknowledgement invalid")
	}
	return nil
}
func BindRemote(ctx context.Context, root string, binding RemoteBinding) error {
	u, err := url.Parse(binding.URL)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost"))) {
		return fmt.Errorf("remote registry requires HTTPS or loopback HTTP")
	}
	if !remoteName(binding.Consumer) || !filepath.IsAbs(binding.TokenFile) {
		return fmt.Errorf("invalid remote consumer binding")
	}
	if _, err = privateToken(binding.TokenFile); err != nil {
		return err
	}
	catalog, err := Catalog(root)
	if err != nil {
		return err
	}
	if catalog != filepath.Join(root, "global-revisions") {
		return fmt.Errorf("remote and shared-path registry bindings cannot be combined")
	}
	if err = os.MkdirAll(catalog, 0700); err != nil {
		return err
	}
	unlock, err := Lock(ctx, catalog)
	if err != nil {
		return err
	}
	defer unlock()
	if _, e := os.Lstat(filepath.Join(root, remoteRootAnchor)); e == nil {
		if _, e = os.Lstat(filepath.Join(catalog, remoteBindingFile)); e != nil {
			return fmt.Errorf("remote catalog binding lost")
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	prior, exists, err := readRemoteBinding(catalog)
	if err != nil {
		return err
	}
	if exists {
		if prior.URL != binding.URL || prior.Consumer != binding.Consumer || prior.TokenFile != binding.TokenFile {
			return fmt.Errorf("remote binding cannot be replaced")
		}
		binding = prior
	} else {
		binding.Incarnation = uuid.NewString()
		binding.Version = remoteProtocol
		if err = writeRegistryRecord(filepath.Join(catalog, remoteBindingFile), binding); err != nil {
			return err
		}
	}
	if err = writeRegistryRecord(filepath.Join(root, remoteRootAnchor), map[string]int{"version": remoteProtocol}); err != nil {
		return err
	}
	binding.catalog = catalog
	if err = writeRegistryRecord(filepath.Join(catalog, remoteBindingAnchor), map[string]int{"version": remoteProtocol}); err != nil {
		return err
	}
	refs, err := scanRemoteReferences(root, catalog)
	if err != nil {
		return err
	}
	return postRemote(ctx, binding, refs, true)
}
func SyncRemote(ctx context.Context, root string) error {
	catalog, err := Catalog(root)
	if err != nil {
		return err
	}
	unlock, err := Lock(ctx, catalog)
	if err != nil {
		return err
	}
	defer unlock()
	binding, exists, err := readRemoteBinding(catalog)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("remote registry not bound")
	}
	refs, err := scanRemoteReferences(root, catalog)
	if err != nil {
		return err
	}
	return postRemote(ctx, binding, refs, true)
}
func scanRemoteReferences(root, catalog string) ([]string, error) {
	refs := map[string]bool{}
	count, total := 0, int64(0)
	err := filepath.WalkDir(root, func(path string, e fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if e.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("remote reference scan rejects symlink")
		}
		if e.IsDir() {
			if name := e.Name(); len(name) == 71 && digest.FindString(name) == name {
				refs[name] = true
			}
			return nil
		}
		name := e.Name()
		if filepath.Dir(path) == catalog && (IsRetirementMetadata(name) || name == remoteBindingFile || name == remoteBindingAnchor || name == remoteConsumerAnchor || name == "consumers.json" || name == remoteConsumersFile) {
			return nil
		}
		if !(strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".jsonl") || strings.HasSuffix(name, ".checkpoint.json.returned")) {
			return nil
		}
		if name == "state.json" {
			return fmt.Errorf("legacy state cannot establish remote completeness")
		}
		if filepath.Dir(path) == catalog {
			id := strings.TrimSuffix(name, ".json")
			if len(id) == 71 && digest.FindString(id) == id {
				retired, err := Retired(catalog, id)
				if err != nil {
					return err
				}
				if retired {
					return nil
				}
			}
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		count++
		total += info.Size()
		if !info.Mode().IsRegular() || count > 10000 || total > 64<<20 {
			return fmt.Errorf("remote inventory unsafe or exceeds budget")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		parts := [][]byte{raw}
		if strings.HasSuffix(name, ".jsonl") {
			parts = bytes.Split(raw, []byte{'\n'})
		}
		for _, part := range parts {
			if strings.HasSuffix(name, ".jsonl") && len(bytes.TrimSpace(part)) == 0 {
				continue
			}
			found, err := References(part)
			if err != nil {
				return err
			}
			for id := range found {
				refs[id] = true
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	result := []string{}
	for id := range refs {
		result = append(result, id)
	}
	sort.Strings(result)
	return result, nil
}

func IsRemoteMetadata(name string) bool {
	return name == remoteBindingFile || name == remoteConsumersFile || name == remoteBindingAnchor || name == remoteConsumerAnchor
}
