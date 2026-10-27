package sshaccess

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gofrs/flock"
	"github.com/oslab/sysbox/pkg/substrate"
)

// Deployment is supplied by a trusted guest adapter, never by a mutation RPC.
// The enrolled main file has one explicit final Include for DropInPath. Reload
// and Ready must address that exact administrator-selected service instance.
type Deployment struct {
	Root, ConfigPath, DropInPath, Helper, CommandUser string
	// ServiceIdentity pins the trusted reload/validation/readiness adapter.
	ServiceIdentity string
	Probes          []SSHDProbe
	Run             SSHDRunner
	Reload          func(context.Context) error
	Ready           func(context.Context) error
}

type deploymentRecord struct {
	Version  int                         `json:"version"`
	Profile  string                      `json:"profile"`
	Main     string                      `json:"main"`
	Previous string                      `json:"previous"`
	Content  []byte                      `json:"content"`
	Configs  []substrate.SSHAccessConfig `json:"configs"`
	Phase    string                      `json:"phase"`
}

const deploymentLimit = 2 << 20

func contentHash(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

func (d *Deployment) identity() (string, error) {
	if d == nil || d.Run == nil || d.Reload == nil || d.Ready == nil || len(d.Probes) == 0 || d.ServiceIdentity == "" {
		return "", fmt.Errorf("complete SSH deployment adapter required")
	}
	for _, path := range []string{d.Root, d.ConfigPath, d.DropInPath} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n\t ") {
			return "", fmt.Errorf("invalid SSH deployment path")
		}
	}
	if d.ConfigPath == d.DropInPath || filepath.Dir(d.ConfigPath) != filepath.Dir(d.DropInPath) || d.Root == filepath.Dir(d.DropInPath) {
		return "", fmt.Errorf("SSH drop-in must be beside the main config with a separate journal directory")
	}
	if _, err := RenderSSHD(d.Helper, d.CommandUser, nil); err != nil {
		return "", err
	}
	raw, err := json.Marshal([]any{d.ConfigPath, d.DropInPath, d.Helper, d.CommandUser, d.Probes, d.ServiceIdentity})
	if err != nil {
		return "", err
	}
	return contentHash(raw), nil
}

func trustedPath(path string, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) && stat.Uid != 0 || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 && !(info.IsDir() && info.Mode()&os.ModeSticky != 0) {
		return fmt.Errorf("untrusted SSH deployment path: %s", path)
	}
	if directory && !info.IsDir() || !directory && (!info.Mode().IsRegular() || stat.Nlink != 1) {
		return fmt.Errorf("invalid SSH deployment file type: %s", path)
	}
	if parent := filepath.Dir(path); parent != path {
		return trustedPath(parent, true)
	}
	return nil
}
func readDeploymentFile(path string) ([]byte, error) {
	if err := trustedPath(path, false); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, deploymentLimit+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > deploymentLimit {
		return nil, fmt.Errorf("SSH deployment file exceeds size limit")
	}
	return raw, nil
}
func atomicDeploymentFile(path string, raw []byte) error {
	if len(raw) > deploymentLimit {
		return fmt.Errorf("SSH deployment record exceeds size limit")
	}
	parent := filepath.Dir(path)
	if err := trustedPath(parent, true); err != nil {
		return err
	}
	file, err := os.CreateTemp(parent, ".sysbox-ssh-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err = file.Write(raw); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Rename(file.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(parent)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (d *Deployment) lock(ctx context.Context) (func(), error) {
	if _, err := d.identity(); err != nil {
		return nil, err
	}
	if err := trustedPath(filepath.Dir(d.Root), true); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(d.Root, 0700); err != nil {
		return nil, err
	}
	if err := trustedPath(d.Root, true); err != nil {
		return nil, err
	}
	info, err := os.Stat(d.Root)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm() != 0700 {
		return nil, fmt.Errorf("SSH deployment journal must be private")
	}
	path := filepath.Join(d.Root, "deployment.lock")
	if err = trustedPath(path, false); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	lock := flock.New(path)
	ok, err := lock.TryLockContext(ctx, 5*time.Millisecond)
	if err != nil || !ok {
		lock.Close()
		if err != nil {
			return nil, err
		}
		return nil, ctx.Err()
	}
	return func() { lock.Close() }, nil
}
func (d *Deployment) load() (deploymentRecord, error) {
	var r deploymentRecord
	raw, err := readDeploymentFile(filepath.Join(d.Root, "deployment.json"))
	if err != nil {
		return r, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&r); err != nil {
		return r, err
	}
	if dec.Decode(new(any)) != io.EOF {
		return r, fmt.Errorf("trailing SSH deployment journal data")
	}
	profile, err := d.identity()
	if err != nil {
		return r, err
	}
	rendered, err := RenderSSHD(d.Helper, d.CommandUser, r.Configs)
	if err != nil || r.Version != 1 || r.Profile != profile || len(r.Main) != 64 || !bytes.Equal(rendered, r.Content) || r.Phase != "prepared" && r.Phase != "installed" && r.Phase != "ready" {
		return r, fmt.Errorf("SSH deployment journal conflict")
	}
	return r, nil
}
func (d *Deployment) save(r deploymentRecord) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return atomicDeploymentFile(filepath.Join(d.Root, "deployment.json"), raw)
}

// stagedConfig is deliberately strict about Includes: validation must use the
// same complete bytes as publication, with no untracked include graph.
func (d *Deployment) stagedConfig(main []byte, stagedDropIn string) ([]byte, error) {
	lines := strings.Split(string(main), "\n")
	include := -1
	for i, line := range lines {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		name := strings.ToLower(fields[0])
		if strings.Contains(name, "=") {
			return nil, fmt.Errorf("SSH enrollment requires space-separated configuration keywords")
		}
		if name == "authorizedkeyscommand" || name == "authorizedkeyscommanduser" {
			return nil, fmt.Errorf("third-party SSH key command conflicts with managed deployment")
		}
		if include >= 0 {
			return nil, fmt.Errorf("managed SSH Include must be the final configuration directive")
		}
		if name == "include" {
			if len(fields) != 2 || fields[1] != d.DropInPath {
				return nil, fmt.Errorf("explicit managed SSH Include required; additional Includes unsupported")
			}
			include = i
		}
	}
	if include < 0 {
		return nil, fmt.Errorf("enroll an explicit final Include for %s", d.DropInPath)
	}
	lines[include] = "Include " + stagedDropIn
	return []byte(strings.Join(lines, "\n")), nil
}
func (d *Deployment) validate(ctx context.Context, main, content []byte, configs []substrate.SSHAccessConfig) error {
	fragment, err := os.CreateTemp(filepath.Dir(d.ConfigPath), ".sysbox-sshd-fragment-")
	if err != nil {
		return err
	}
	defer os.Remove(fragment.Name())
	defer fragment.Close()
	if _, err = fragment.Write(content); err != nil {
		return err
	}
	if err = fragment.Close(); err != nil {
		return err
	}
	raw, err := d.stagedConfig(main, fragment.Name())
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(d.ConfigPath), ".sysbox-sshd-check-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err = file.Write(raw); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return ValidateSSHD(ctx, d.Run, file.Name(), d.Helper, d.CommandUser, configs, d.Probes)
}
func (d *Deployment) currentDropIn() (string, error) {
	raw, err := readDeploymentFile(d.DropInPath)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return contentHash(raw), nil
}

// Ensure completes a durable deployment before access keys may be published.
// An interrupted reload stays pending. Recovery never overwrites foreign edits.
func (d *Deployment) Ensure(ctx context.Context, configs []substrate.SSHAccessConfig) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	unlock, err := d.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	prior, err := d.load()
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	missing := os.IsNotExist(err)
	if !missing && prior.Phase != "ready" {
		if err = d.reconcile(ctx, &prior); err != nil {
			return err
		}
	}
	main, err := readDeploymentFile(d.ConfigPath)
	if err != nil {
		return err
	}
	previous, err := d.currentDropIn()
	if err != nil {
		return err
	}
	if missing && previous != "" || !missing && (prior.Main != contentHash(main) || previous != contentHash(prior.Content)) {
		return fmt.Errorf("SSH deployment ownership or administrator configuration conflict")
	}
	// Keys remain in the guest CAS store. The service journal needs only endpoint
	// rules, and must not duplicate mutable access data or its history.
	endpoints := make([]substrate.SSHAccessConfig, len(configs))
	for i, cfg := range configs {
		endpoints[i] = substrate.SSHAccessConfig{BindIP: cfg.BindIP, Port: cfg.Port}
	}
	content, err := RenderSSHD(d.Helper, d.CommandUser, endpoints)
	if err != nil {
		return err
	}
	if err = d.validate(ctx, main, content, endpoints); err != nil {
		return err
	}
	profile, err := d.identity()
	if err != nil {
		return err
	}
	r := deploymentRecord{Version: 1, Profile: profile, Main: contentHash(main), Previous: previous, Content: content, Configs: endpoints, Phase: "prepared"}
	if err = d.save(r); err != nil {
		return err
	}
	return d.reconcile(ctx, &r)
}
func (d *Deployment) reconcile(ctx context.Context, r *deploymentRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	main, err := readDeploymentFile(d.ConfigPath)
	if err != nil {
		return err
	}
	if r.Main != contentHash(main) {
		return fmt.Errorf("administrator SSH configuration changed during deployment")
	}
	current, err := d.currentDropIn()
	if err != nil {
		return err
	}
	if current != contentHash(r.Content) && (r.Phase != "prepared" || current != r.Previous) {
		return fmt.Errorf("managed SSH drop-in changed outside deployment")
	}
	if err = d.validate(ctx, main, r.Content, r.Configs); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	latestMain, err := readDeploymentFile(d.ConfigPath)
	if err != nil {
		return err
	}
	latestDrop, err := d.currentDropIn()
	if err != nil {
		return err
	}
	if contentHash(latestMain) != r.Main || latestDrop != current {
		return fmt.Errorf("SSH configuration changed during validation")
	}
	if current != contentHash(r.Content) {
		if err = atomicDeploymentFile(d.DropInPath, r.Content); err != nil {
			return err
		}
	}
	r.Phase = "installed"
	if err = d.save(*r); err != nil {
		return err
	}
	// Re-read both files after publication; never reload a known conflicting config.
	check, err := readDeploymentFile(d.ConfigPath)
	if err != nil {
		return err
	}
	current, err = d.currentDropIn()
	if err != nil {
		return err
	}
	if contentHash(check) != r.Main || current != contentHash(r.Content) {
		return fmt.Errorf("SSH configuration changed before reload")
	}
	if err = ValidateSSHD(ctx, d.Run, d.ConfigPath, d.Helper, d.CommandUser, r.Configs, d.Probes); err != nil {
		return err
	}
	check, err = readDeploymentFile(d.ConfigPath)
	if err != nil {
		return err
	}
	current, err = d.currentDropIn()
	if err != nil {
		return err
	}
	if contentHash(check) != r.Main || current != contentHash(r.Content) {
		return fmt.Errorf("SSH configuration changed during installed validation")
	}
	if err = d.Reload(ctx); err != nil {
		return fmt.Errorf("SSH reload: %w", err)
	}
	if err = d.awaitReady(ctx); err != nil {
		return fmt.Errorf("SSH service readiness: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	check, err = readDeploymentFile(d.ConfigPath)
	if err != nil {
		return err
	}
	current, err = d.currentDropIn()
	if err != nil {
		return err
	}
	if contentHash(check) != r.Main || current != contentHash(r.Content) {
		return fmt.Errorf("SSH configuration changed during reload")
	}
	r.Phase = "ready"
	return d.save(*r)
}

func (d *Deployment) awaitReady(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		err := d.Ready(ctx)
		if err == nil {
			return ctx.Err()
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(ctx.Err(), err)
		case <-timer.C:
		}
	}
}
func (d *Deployment) Recover(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	unlock, err := d.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	r, err := d.load()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return d.reconcile(ctx, &r)
}
