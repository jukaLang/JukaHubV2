//go:build windows

package main

// vpn_windows.go — the VPN's platform half on Windows: the tunnel is
// WireGuard for Windows' own tunnel service, which routes the whole PC
// through it (a Windows tunnel needs the Wintun driver and an administrator,
// which WireGuard's service already has).
//
// Installing or removing the service needs the administrator, so JukaHub
// starts an elevated copy of itself — one Windows permission prompt that
// names JukaHub — with `--vpn-wg-up REQUEST SHA256` or `--vpn-wg-down`, and
// waits for it: its exit status is the answer (vpnWinExitMessage), so the
// elevated copy never writes into a folder the user controls. The request
// (the normalised configuration) waits in JukaHub's data folder; the elevated
// copy refuses it unless its SHA-256 is the one on its command line, parses
// it again, and writes the file WireGuard reads into
// %ProgramData%\JukaHub\vpn, a folder only the system and administrators can
// use — checked through a handle that does not follow links, so a junction a
// user planted there is refused. wireguard.exe is the one in the Program
// Files known folder (not the environment, which a user can change for an
// elevated child) and must belong to an administrator, the system or
// TrustedInstaller.
//
// Nothing here prompts on its own: the service is changed only when the user
// switches the VPN, picks what goes through it or imports a configuration
// (vpnPlatformAuto is false). The service starts with Windows like any
// WireGuard tunnel, until it is switched off here or in WireGuard.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	vpnWinTunnel  = "JukaHub"
	vpnWinService = "WireGuardTunnel$" + vpnWinTunnel
	// The NT SERVICE\TrustedInstaller account, which owns files Windows
	// installs.
	vpnWinTrustedInstaller = "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"
	// vpnWinProtected is the folder WireGuard's file goes into: owned by the
	// Administrators group (whatever a policy says new objects' owner is),
	// SYSTEM and Administrators only, not inherited from ProgramData.
	vpnWinProtected = "O:BAD:PAI(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
)

// The elevated copy's exit statuses.
const (
	vpnWinExitOK        = 0
	vpnWinExitUsage     = 2
	vpnWinExitRequest   = 10
	vpnWinExitConfig    = 11
	vpnWinExitNoWG      = 12
	vpnWinExitFolder    = 13
	vpnWinExitInstall   = 14
	vpnWinExitUninstall = 15
)

func vpnWinExitMessage(code uint32) string {
	switch code {
	case vpnWinExitRequest:
		return "the request changed before Windows allowed it; try again"
	case vpnWinExitConfig:
		return "the configuration did not pass the check"
	case vpnWinExitNoWG:
		return "WireGuard for Windows is missing or not installed where Windows keeps programs"
	case vpnWinExitFolder:
		return `%ProgramData%\JukaHub could not be made private; delete it and try again`
	case vpnWinExitInstall:
		return "WireGuard did not take the tunnel"
	case vpnWinExitUninstall:
		return "WireGuard did not remove the tunnel"
	}
	return fmt.Sprintf("the helper stopped with status %d", code)
}

// vpnWinRequest is what the elevated copy is asked to install.
type vpnWinRequest struct {
	Conf string `json:"conf"`
}

// handleVPNCommand runs the elevated helper when JukaHub was started as one.
func handleVPNCommand(args []string) (handled bool, exitCode int) {
	if len(args) == 0 || (args[0] != "--vpn-wg-up" && args[0] != "--vpn-wg-down") {
		return false, 0
	}
	return true, vpnWinElevated(args)
}

// vpnWinWireGuardExe finds WireGuard for Windows where its installer puts it.
func vpnWinWireGuardExe() string {
	dir, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, 0)
	if err != nil {
		return ""
	}
	p := filepath.Join(dir, "WireGuard", "wireguard.exe")
	if fi, err := os.Stat(p); err != nil || fi.IsDir() {
		return ""
	}
	return p
}

// vpnWinFound caches whether WireGuard is installed: the Settings rows ask
// often, and the answer changes only when it is installed or removed.
var vpnWinFound struct {
	sync.Mutex
	at time.Time
	ok bool
}

func vpnPlatformSupport() (bool, string) {
	vpnWinFound.Lock()
	if vpnWinFound.at.IsZero() || time.Since(vpnWinFound.at) > 5*time.Second {
		vpnWinFound.ok, vpnWinFound.at = vpnWinWireGuardExe() != "", time.Now()
	}
	ok := vpnWinFound.ok
	vpnWinFound.Unlock()
	if !ok {
		return false, "Needs WireGuard for Windows (wireguard.com/install); JukaHub hands it the tunnel"
	}
	return true, ""
}

func vpnPlatformAuto() bool { return false }

func vpnPlatformRecover() {}

// vpnWinDir is JukaHub's own VPN folder in its data folder: the request
// while the elevated copy reads it, and the hash of what was installed.
func vpnWinDir() (string, error) {
	path, err := vpnProfilePath()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(filepath.Dir(path), "vpn")
	return dir, os.MkdirAll(dir, 0o700)
}

// vpnWinApplied is the payload hash JukaHub last installed, or "".
func vpnWinApplied() string {
	dir, err := vpnWinDir()
	if err != nil {
		return ""
	}
	var a struct {
		Hash string `json:"hash"`
	}
	if data, err := os.ReadFile(filepath.Join(dir, "applied.json")); err == nil {
		_ = json.Unmarshal(data, &a)
	}
	return a.Hash
}

func vpnPlatformApply(payload []byte) error {
	var p vpnPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return err
	}
	cfg, err := wgParseConfig(p.Config)
	if err != nil {
		return err
	}
	dir, err := vpnWinDir()
	if err != nil {
		return err
	}
	req, err := json.Marshal(vpnWinRequest{Conf: cfg.quickText(p.RouteAll)})
	if err != nil {
		return err
	}
	reqPath := filepath.Join(dir, "request.json")
	if err := os.WriteFile(reqPath, req, 0o600); err != nil {
		return err
	}
	sum := sha256.Sum256(req)
	code, err := vpnWinElevate("--vpn-wg-up", reqPath, hex.EncodeToString(sum[:]))
	_ = os.Remove(reqPath) // it holds the private key
	if err != nil {
		return err
	}
	if code != vpnWinExitOK {
		return errors.New(vpnWinExitMessage(code))
	}
	applied, _ := json.Marshal(map[string]string{"hash": vpnPayloadHash(payload)})
	return os.WriteFile(filepath.Join(dir, "applied.json"), applied, 0o600)
}

func vpnPlatformStop() error {
	code, err := vpnWinElevate("--vpn-wg-down")
	if err != nil {
		return err
	}
	if code != vpnWinExitOK {
		return errors.New(vpnWinExitMessage(code))
	}
	if dir, err := vpnWinDir(); err == nil {
		_ = os.Remove(filepath.Join(dir, "applied.json"))
	}
	return nil
}

var (
	vpnWinShell32        = windows.NewLazySystemDLL("shell32.dll")
	vpnWinShellExecuteEx = vpnWinShell32.NewProc("ShellExecuteExW")
)

// vpnWinShellExecuteInfo is SHELLEXECUTEINFOW (x/sys/windows has no
// ShellExecuteEx); Go lays it out as the 64-bit ABI does.
type vpnWinShellExecuteInfo struct {
	cbSize         uint32
	fMask          uint32
	hwnd           windows.Handle
	lpVerb         *uint16
	lpFile         *uint16
	lpParameters   *uint16
	lpDirectory    *uint16
	nShow          int32
	hInstApp       windows.Handle
	lpIDList       uintptr
	lpClass        *uint16
	hkeyClass      windows.Handle
	dwHotKey       uint32
	hIconOrMonitor windows.Handle
	hProcess       windows.Handle
}

const (
	vpnWinSeeMaskNoCloseProcess = 0x00000040
	vpnWinSeeMaskNoAsync        = 0x00000100
)

// vpnWinElevate runs an elevated copy of JukaHub with args and waits for it
// (at most five minutes). Windows asks the user first; a "No" comes back as
// ERROR_CANCELLED.
func vpnWinElevate(args ...string) (uint32, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = windows.EscapeArg(a)
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return 0, err
	}
	params, err := windows.UTF16PtrFromString(strings.Join(quoted, " "))
	if err != nil {
		return 0, err
	}
	dir, _ := windows.UTF16PtrFromString(filepath.Dir(exe))
	info := vpnWinShellExecuteInfo{
		fMask:        vpnWinSeeMaskNoCloseProcess | vpnWinSeeMaskNoAsync,
		lpVerb:       verb,
		lpFile:       file,
		lpParameters: params,
		lpDirectory:  dir,
		nShow:        windows.SW_HIDE,
	}
	info.cbSize = uint32(unsafe.Sizeof(info))
	if r, _, callErr := vpnWinShellExecuteEx.Call(uintptr(unsafe.Pointer(&info))); r == 0 {
		if errors.Is(callErr, windows.ERROR_CANCELLED) {
			return 0, errors.New("Windows asked for permission and it was not given")
		}
		return 0, callErr
	}
	if info.hProcess == 0 {
		return 0, errors.New("Windows did not start the helper")
	}
	defer windows.CloseHandle(info.hProcess)
	if ev, err := windows.WaitForSingleObject(info.hProcess, 5*60*1000); err != nil || ev != windows.WAIT_OBJECT_0 {
		return 0, errors.New("the helper did not finish")
	}
	var code uint32
	if err := windows.GetExitCodeProcess(info.hProcess, &code); err != nil {
		return 0, err
	}
	return code, nil
}

// vpnWinServiceState is the tunnel service's state (0 when not installed).
func vpnWinServiceState() uint32 {
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return 0
	}
	defer windows.CloseServiceHandle(scm)
	name, _ := windows.UTF16PtrFromString(vpnWinService)
	svc, err := windows.OpenService(scm, name, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return 0
	}
	defer windows.CloseServiceHandle(svc)
	var st windows.SERVICE_STATUS
	if windows.QueryServiceStatus(svc, &st) != nil {
		return 0
	}
	return st.CurrentState
}

func vpnPlatformProbe() vpnProbe {
	var p vpnProbe
	switch vpnWinServiceState() {
	case windows.SERVICE_RUNNING:
		p.Running, p.State = true, vpnStateActive
		p.Detail = "WireGuard for Windows carries the PC's traffic"
		p.Hash = vpnWinApplied()
		if iface, err := net.InterfaceByName(vpnWinTunnel); err == nil {
			if addrs, err := iface.Addrs(); err == nil {
				for _, a := range addrs {
					p.Addresses = append(p.Addresses, a.String())
				}
			}
		}
	case windows.SERVICE_START_PENDING, windows.SERVICE_CONTINUE_PENDING:
		p.Running, p.State = true, vpnStateStarting
		p.Hash = vpnWinApplied()
	case windows.SERVICE_STOP_PENDING:
		p.Running = true
	case windows.SERVICE_STOPPED, windows.SERVICE_PAUSED, windows.SERVICE_PAUSE_PENDING:
		// Installed and set to start with Windows, but not running: still
		// something to remove when the VPN is off.
		p.Installed, p.State = true, vpnStateError
		p.Detail = "WireGuard's tunnel service stopped; switch the VPN off and on to start it again"
		p.Hash = vpnWinApplied()
	}
	if p.Running {
		p.Installed = true
	}
	return p
}

// ── The elevated copy ────────────────────────────────────────────────────

func vpnWinElevated(args []string) int {
	switch {
	case args[0] == "--vpn-wg-up" && len(args) == 3:
		return vpnWinUp(args[1], args[2])
	case args[0] == "--vpn-wg-down" && len(args) == 1:
		return vpnWinDown()
	}
	return vpnWinExitUsage
}

// vpnWinTrustedExe finds wireguard.exe and checks it belongs to an
// administrator, the system or TrustedInstaller — no user could have put it
// there or changed it.
func vpnWinTrustedExe() string {
	exe := vpnWinWireGuardExe()
	if exe == "" {
		return ""
	}
	sd, err := windows.GetNamedSecurityInfo(exe, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return ""
	}
	owner, _, err := sd.Owner()
	if err != nil || !(owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) ||
		owner.IsWellKnown(windows.WinLocalSystemSid) || owner.String() == vpnWinTrustedInstaller) {
		return ""
	}
	return exe
}

// vpnWinWaitGone waits up to 30 s for the tunnel service to be removed.
func vpnWinWaitGone() bool {
	for i := 0; i < 150; i++ {
		if vpnWinServiceState() == 0 {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

// vpnWinUp installs the request as the tunnel service, replacing any earlier
// one.
func vpnWinUp(reqPath, wantSum string) int {
	if filepath.Base(reqPath) != "request.json" {
		return vpnWinExitRequest
	}
	data, err := os.ReadFile(reqPath)
	if err != nil || len(data) > 3*wgConfigMaxBytes {
		return vpnWinExitRequest
	}
	if sum := sha256.Sum256(data); !strings.EqualFold(hex.EncodeToString(sum[:]), wantSum) {
		return vpnWinExitRequest
	}
	var req vpnWinRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return vpnWinExitRequest
	}
	cfg, err := wgParseConfig(req.Conf)
	if err != nil {
		return vpnWinExitConfig
	}
	exe := vpnWinTrustedExe()
	if exe == "" {
		return vpnWinExitNoWG
	}
	// The old tunnel goes first: its service reads the same file, so writing
	// the new one before it is gone could start the new setup under the old
	// name after a reboot.
	if vpnWinServiceState() != 0 {
		_ = exec.Command(exe, "/uninstalltunnelservice", vpnWinTunnel).Run()
		if !vpnWinWaitGone() {
			return vpnWinExitUninstall
		}
	}
	confPath, err := vpnWinWriteConf(cfg.quickText(false))
	if err != nil {
		return vpnWinExitFolder
	}
	if err := exec.Command(exe, "/installtunnelservice", confPath).Run(); err != nil {
		return vpnWinExitInstall
	}
	return vpnWinExitOK
}

func vpnWinDown() int {
	exe := vpnWinTrustedExe()
	if exe == "" {
		return vpnWinExitNoWG
	}
	if vpnWinServiceState() != 0 {
		_ = exec.Command(exe, "/uninstalltunnelservice", vpnWinTunnel).Run()
		if !vpnWinWaitGone() {
			return vpnWinExitUninstall
		}
	}
	if dir, err := vpnWinConfDir(); err == nil {
		_ = os.Remove(filepath.Join(dir, vpnWinTunnel+".conf"))
	}
	return vpnWinExitOK
}

// vpnWinConfDir is %ProgramData%\JukaHub\vpn, from the known folder.
func vpnWinConfDir() (string, error) {
	base, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0)
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "JukaHub", "vpn"), nil
}

// vpnWinWriteConf writes the configuration WireGuard's service reads where
// only the system and administrators can read or change it. Whatever had the
// name is removed first and the file made new, so a link left there cannot
// redirect the write.
func vpnWinWriteConf(text string) (string, error) {
	dir, err := vpnWinConfDir()
	if err != nil {
		return "", err
	}
	sd, err := windows.SecurityDescriptorFromString(vpnWinProtected)
	if err != nil {
		return "", err
	}
	if err := vpnWinTrustedBase(filepath.Dir(filepath.Dir(dir))); err != nil {
		return "", err
	}
	for _, d := range []string{filepath.Dir(dir), dir} {
		if err := vpnWinAdminDir(d, sd); err != nil {
			return "", err
		}
	}
	path := filepath.Join(dir, vpnWinTunnel+".conf")
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	_, err = f.WriteString(text)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return path, err
}

// vpnWinTrustedBase checks ProgramData itself — a real folder (not a link)
// owned by the system, the administrators or TrustedInstaller — without
// changing anything about it.
func vpnWinTrustedBase(dir string) error {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(p, windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("%s is a link", dir)
	}
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	if !owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) && !owner.IsWellKnown(windows.WinLocalSystemSid) &&
		owner.String() != vpnWinTrustedInstaller {
		return fmt.Errorf("%s is not owned by Windows", dir)
	}
	return nil
}

// vpnWinAdminDir makes dir a folder only SYSTEM and Administrators may use.
// It is checked through a handle opened without following links: a junction
// is refused, and a folder that already exists must belong to an
// administrator or the system — any user may make folders in ProgramData.
func vpnWinAdminDir(dir string, sd *windows.SECURITY_DESCRIPTOR) error {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	sa := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	if err := windows.CreateDirectory(p, sa); err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return err
	}
	h, err := windows.CreateFile(p, windows.READ_CONTROL|windows.WRITE_DAC,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return fmt.Errorf("%s is a link or not a folder", dir)
	}
	cur, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := cur.Owner()
	if err != nil {
		return err
	}
	if !owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) && !owner.IsWellKnown(windows.WinLocalSystemSid) {
		return fmt.Errorf("%s was not made by an administrator", dir)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetSecurityInfo(h, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}
