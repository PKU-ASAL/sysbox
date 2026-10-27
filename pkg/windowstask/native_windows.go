package windowstask

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/taskprotocol"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const DefaultConfigPath = `C:\ProgramData\Sysbox\guest-task\config.json`

type WindowsConfig struct {
	Root           string                      `json:"root"`
	PublicKey      []byte                      `json:"public_key"`
	Scope          controlplane.ExecutionScope `json:"scope"`
	WorkerUser     string                      `json:"worker_user"`
	WorkerPassword string                      `json:"worker_password"`
	WorkerDomain   string                      `json:"worker_domain"`
}
type WindowsPlatform struct {
	HelperBinary string
	Config       WindowsConfig
	ConfigPath   string
	engine       *Engine
}

func LoadWindowsEngine(path string) (*Engine, error) {
	p := &WindowsPlatform{ConfigPath: path}
	if err := p.ValidateRoot(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if err := p.ValidateFile(path); err != nil {
		return nil, err
	}
	binary, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if err = p.ValidateFile(binary); err != nil {
		return nil, err
	}
	if err := readJSON(path, &p.Config); err != nil {
		return nil, err
	}
	if len(p.Config.PublicKey) != ed25519.PublicKeySize || !filepath.IsAbs(p.Config.Root) {
		return nil, ErrUnauthorized
	}
	if err := p.ValidateRoot(p.Config.Root); err != nil {
		return nil, err
	}
	e := &Engine{Root: p.Config.Root, PublicKey: p.Config.PublicKey, Scope: p.Config.Scope, Platform: p}
	p.engine = e
	// Do not expose a new info boot token to the host when an older helper's
	// journal exists: generic host recovery also treats boot change as stop proof.
	records, err := e.list()
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		if !p.ValidBootID(record.BootID) {
			return nil, fmt.Errorf("legacy Windows boot identity requires independent fencing and explicit journal rebuild")
		}
	}
	return e, nil
}

// The firmware boot-environment GUID is not a kernel boot identifier on all
// Windows images. A private REG_OPTION_VOLATILE key survives process/service
// restarts but is discarded when the kernel's registry hives are reloaded.
// An interrupted initialization fails closed until reboot; it never regenerates
// an identifier in an existing key and therefore cannot invent a stop proof.
func (*WindowsPlatform) BootID() (string, error) {
	sd, err := windows.SecurityDescriptorFromString("O:SYG:SYD:P(A;;KA;;;SY)(A;;KA;;;BA)")
	if err != nil {
		return "", err
	}
	attrs := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	path, err := windows.UTF16PtrFromString(`SYSTEM\CurrentControlSet\Control\SysboxTaskBootV1`)
	if err != nil {
		return "", err
	}
	var handle windows.Handle
	var disposition uint32
	status, _, _ := windows.NewLazySystemDLL("advapi32.dll").NewProc("RegCreateKeyExW").Call(uintptr(windows.HKEY_LOCAL_MACHINE), uintptr(unsafe.Pointer(path)), 0, 0, 1, uintptr(registry.ALL_ACCESS), uintptr(unsafe.Pointer(&attrs)), uintptr(unsafe.Pointer(&handle)), uintptr(unsafe.Pointer(&disposition)))
	if status != 0 {
		return "", syscall.Errno(status)
	}
	key := registry.Key(handle)
	defer key.Close()
	actual, err := windows.GetSecurityInfo(handle, windows.SE_REGISTRY_KEY, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return "", err
	}
	owner, _, err := actual.Owner()
	if err != nil || (owner.String() != "S-1-5-18" && owner.String() != "S-1-5-32-544") {
		return "", ErrUnauthorized
	}
	acl, _, err := actual.DACL()
	if err != nil || acl == nil {
		return "", ErrUnauthorized
	}
	control, _, err := actual.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return "", ErrUnauthorized
	}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err = windows.GetAce(acl, i, &ace); err != nil {
			return "", err
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return "", ErrUnauthorized
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		if sid != "S-1-5-18" && sid != "S-1-5-32-544" {
			return "", ErrUnauthorized
		}
	}
	if disposition == 1 {
		var nonce [32]byte
		if _, err = rand.Read(nonce[:]); err != nil {
			return "", err
		}
		if err = key.SetStringValue("Identity", "windows-volatile-v1-"+hex.EncodeToString(nonce[:])); err != nil {
			return "", err
		}
	}
	value, _, err := key.GetStringValue("Identity")
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(value, "windows-volatile-v1-") || len(value) != len("windows-volatile-v1-")+64 {
		return "", ErrUnauthorized
	}
	if _, err = hex.DecodeString(strings.TrimPrefix(value, "windows-volatile-v1-")); err != nil {
		return "", ErrUnauthorized
	}
	return value, nil
}
func (*WindowsPlatform) Uptime() time.Duration { return windows.DurationSinceBoot() }

// The helper, its credentials and journal must not be writable/readable by the
// workload account. Reject unprotected ACLs and any non-admin allow ACE.
func (p *WindowsPlatform) ValidateRoot(path string) error { return p.validatePrivate(path, true) }
func (p *WindowsPlatform) ValidateFile(path string) error { return p.validatePrivate(path, false) }
func (*WindowsPlatform) validatePrivate(path string, protected bool) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	for part := absolute; ; part = filepath.Dir(part) {
		name, err := windows.UTF16PtrFromString(part)
		if err != nil {
			return err
		}
		attrs, err := windows.GetFileAttributes(name)
		if err != nil {
			return err
		}
		if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return ErrUnauthorized
		}
		if part != absolute {
			if err := validateAncestorReplacement(part); err != nil {
				return err
			}
		}
		next := filepath.Dir(part)
		if next == part {
			break
		}
	}
	sd, err := windows.GetNamedSecurityInfo(absolute, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		return ErrUnauthorized
	}
	control, _, err := sd.Control()
	if err != nil || protected && control&windows.SE_DACL_PROTECTED == 0 {
		return ErrUnauthorized
	}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err = windows.GetAce(acl, i, &ace); err != nil {
			return err
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return ErrUnauthorized
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		if sid != "S-1-5-18" && sid != "S-1-5-32-544" {
			return ErrUnauthorized
		}
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	if owner.String() != "S-1-5-18" && owner.String() != "S-1-5-32-544" {
		return ErrUnauthorized
	}
	return nil
}
func (p *WindowsPlatform) workerToken() (windows.Token, error) {
	if p.Config.WorkerUser == "" || p.Config.WorkerPassword == "" {
		return 0, fmt.Errorf("configure a dedicated non-administrator worker account")
	}
	domain := p.Config.WorkerDomain
	if domain == "" {
		domain = "."
	}
	user, err := windows.UTF16PtrFromString(p.Config.WorkerUser)
	if err != nil {
		return 0, err
	}
	dom, err := windows.UTF16PtrFromString(domain)
	if err != nil {
		return 0, err
	}
	pass, err := windows.UTF16PtrFromString(p.Config.WorkerPassword)
	if err != nil {
		return 0, err
	}
	var token windows.Token
	ok, _, callErr := windows.NewLazySystemDLL("advapi32.dll").NewProc("LogonUserW").Call(uintptr(unsafe.Pointer(user)), uintptr(unsafe.Pointer(dom)), uintptr(unsafe.Pointer(pass)), 2, 0, uintptr(unsafe.Pointer(&token)))
	if ok == 0 {
		return 0, callErr
	}
	valid := false
	defer func() {
		if !valid {
			token.Close()
		}
	}()
	identity, err := token.GetTokenUser()
	if err != nil {
		return 0, err
	}
	if identity.User.Sid.String() == "S-1-5-18" {
		return 0, ErrUnauthorized
	}
	groups, err := token.GetTokenGroups()
	if err != nil {
		return 0, err
	}
	for _, group := range groups.AllGroups() {
		if group.Sid.String() == "S-1-5-32-544" {
			return 0, fmt.Errorf("worker account must not be an administrator")
		}
	}
	valid = true
	return token, nil
}
func (p *WindowsPlatform) Launch(ctx context.Context, request taskprotocol.Request) error {
	binary, err := os.Executable()
	if p.HelperBinary != "" {
		binary = p.HelperBinary
	}
	if err != nil {
		return err
	}
	inputR, inputW, err := os.Pipe()
	if err != nil {
		return err
	}
	defer inputR.Close()
	defer inputW.Close()
	ackR, ackW, err := os.Pipe()
	if err != nil {
		return err
	}
	defer ackR.Close()
	defer ackW.Close()
	waitCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	stop := context.AfterFunc(waitCtx, func() { inputW.Close(); ackR.Close() })
	defer stop()
	log, err := os.OpenFile(filepath.Join(p.engine.Root, request.ID, "worker.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	cmd := exec.Command(binary, "--worker", "--config", p.ConfigPath)
	cmd.Stdin = inputR
	cmd.Stdout = ackW
	cmd.Stderr = log
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_BREAKAWAY_FROM_JOB, HideWindow: true}
	if err = cmd.Start(); err != nil {
		return err
	}
	inputR.Close()
	ackW.Close()
	go cmd.Wait()
	if err = json.NewEncoder(inputW).Encode(request); err != nil {
		return err
	}
	inputW.Close()
	acknowledged := make(chan error, 1)
	go func() {
		line, err := bufio.NewReader(ackR).ReadString('\n')
		if err == nil && line != "READY\n" {
			err = ErrUnknown
		}
		acknowledged <- err
	}()
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	select {
	case err := <-acknowledged:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return fmt.Errorf("%w: worker acknowledgement missing", ErrUnknown)
	}
}
func taskJobName(r taskprotocol.Record) string {
	return fmt.Sprintf(`Local\SysboxTask-%x`, sha256.Sum256([]byte(r.BootID+"\x00"+r.ID+"\x00"+r.Digest)))
}
func openTaskJob(r taskprotocol.Record, access uint32) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(taskJobName(r))
	if err != nil {
		return 0, err
	}
	h, _, err := windows.NewLazySystemDLL("kernel32.dll").NewProc("OpenJobObjectW").Call(uintptr(access), 0, uintptr(unsafe.Pointer(name)))
	if h == 0 {
		return 0, err
	}
	return windows.Handle(h), nil
}

type jobAccounting struct {
	User, Kernel, PeriodUser, PeriodKernel int64
	PageFaults, Total, Active, Terminated  uint32
}

func jobEmpty(job windows.Handle) (bool, error) {
	var info jobAccounting
	err := windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil)
	return info.Active == 0, err
}
func processCreation(pid int) (uint64, bool, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	defer windows.CloseHandle(h)
	var created, exited, kernel, user windows.Filetime
	if err = windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return 0, false, err
	}
	code, err := windows.WaitForSingleObject(h, 0)
	if err != nil {
		return 0, false, err
	}
	return uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime), code == uint32(windows.WAIT_TIMEOUT), nil
}
func (p *WindowsPlatform) Inspect(ctx context.Context, r taskprotocol.Record) (taskprotocol.Record, error) {
	if r.SupervisorPID == 0 {
		return r, nil
	}
	created, alive, err := processCreation(r.SupervisorPID)
	if err != nil {
		return r, err
	}
	if alive && created == r.SupervisorStartTicks {
		return r, nil
	}
	// Do not infer an exit status from supervisor disappearance. Job Object
	// absence is deliberately conservative; an explicit stop or reboot settles it.
	return p.Stop(ctx, r)
}
func (p *WindowsPlatform) Stop(ctx context.Context, r taskprotocol.Record) (taskprotocol.Record, error) {
	job, err := openTaskJob(r, 0x0004|0x0008)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		r.Status = "unknown"
		r.ExitCodeKnown = false
		r.Error = "job unavailable after supervisor loss; reboot proof required"
		return r, nil
	}
	if err != nil {
		return r, err
	}
	defer windows.CloseHandle(job)
	if err = windows.TerminateJobObject(job, 1); err != nil {
		return r, err
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		empty, err := jobEmpty(job)
		if err != nil {
			return r, err
		}
		if empty {
			r.Status = "unknown"
			r.ExitCodeKnown = false
			r.TerminationConfirmed = true
			return r, nil
		}
		select {
		case <-ctx.Done():
			return r, ctx.Err()
		case <-deadline.C:
			return r, ErrUnknown
		case <-tick.C:
		}
	}
}
func windowsEnvironment(extra map[string]string) ([]uint16, error) {
	env := map[string]string{}
	for _, k := range []string{"SystemRoot", "WINDIR", "SystemDrive", "ComSpec", "PATH", "PATHEXT", "TEMP", "TMP"} {
		if v := os.Getenv(k); v != "" {
			env[strings.ToUpper(k)] = v
		}
	}
	for k, v := range extra {
		env[strings.ToUpper(k)] = v
	}
	var parts []string
	for k, v := range env {
		parts = append(parts, k+"="+v)
	}
	// Windows expects the Unicode environment block sorted case-insensitively.
	sort.Strings(parts)
	var result []uint16
	for _, p := range parts {
		v, err := windows.UTF16FromString(p)
		if err != nil {
			return nil, err
		}
		result = append(result, v...)
	}
	return append(result, 0), nil
}

func trustedWindowsOwner(sid *windows.SID) bool {
	text := sid.String()
	if text == "S-1-5-18" || text == "S-1-5-32-544" {
		return true
	}
	installer, _, _, err := windows.LookupSID("", "NT SERVICE\\TrustedInstaller")
	return err == nil && sid.Equals(installer)
}
func validateAncestorReplacement(path string) error {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil || !trustedWindowsOwner(owner) {
		return ErrUnauthorized
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		return ErrUnauthorized
	}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err = windows.GetAce(acl, i, &ace); err != nil {
			return err
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 || ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return ErrUnauthorized
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if trustedWindowsOwner(sid) {
			continue
		}
		const replacementMask = uint32(windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER | 0x40 | windows.GENERIC_ALL)
		if uint32(ace.Mask)&replacementMask != 0 {
			return ErrUnauthorized
		}
	}
	return nil
}

func (*WindowsPlatform) ValidBootID(id string) bool {
	const prefix = "windows-volatile-v1-"
	if !strings.HasPrefix(id, prefix) || len(id) != len(prefix)+64 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(id, prefix))
	return err == nil
}
