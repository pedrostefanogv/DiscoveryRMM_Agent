//go:build windows

package platform

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestInspectFileDACL_SyntheticACEs(t *testing.T) {
	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		t.Fatalf("WinLocalSystemSid: %v", err)
	}
	adminSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		t.Fatalf("WinBuiltinAdministratorsSid: %v", err)
	}
	usersSID, err := windows.CreateWellKnownSid(windows.WinBuiltinUsersSid)
	if err != nil {
		t.Fatalf("WinBuiltinUsersSid: %v", err)
	}

	var pinner runtime.Pinner
	pinner.Pin(systemSID)
	pinner.Pin(adminSID)
	pinner.Pin(usersSID)
	defer pinner.Unpin()

	if err := inspectFileDACL(nil, true, false); err == nil {
		t.Fatalf("expected nil DACL to be rejected")
	}

	const fileAllAccess = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1FF

	strictACL, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{
		{
			AccessPermissions: fileAllAccess,
			AccessMode:        windows.SET_ACCESS,
			Inheritance:       windows.NO_INHERITANCE,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
				TrusteeValue: windows.TrusteeValueFromSID(systemSID),
			},
		},
		{
			AccessPermissions: fileAllAccess,
			AccessMode:        windows.SET_ACCESS,
			Inheritance:       windows.NO_INHERITANCE,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_ALIAS,
				TrusteeValue: windows.TrusteeValueFromSID(adminSID),
			},
		},
	}, nil)
	if err != nil {
		t.Fatalf("ACLFromEntries(strict): %v", err)
	}
	if err := inspectFileDACL(strictACL, true, false); err != nil {
		t.Fatalf("expected SYSTEM+Admins DACL to pass strict check: %v", err)
	}

	usersReadACL, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{
		{
			AccessPermissions: fileAllAccess,
			AccessMode:        windows.SET_ACCESS,
			Inheritance:       windows.NO_INHERITANCE,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
				TrusteeValue: windows.TrusteeValueFromSID(systemSID),
			},
		},
		{
			AccessPermissions: windows.FILE_GENERIC_READ,
			AccessMode:        windows.SET_ACCESS,
			Inheritance:       windows.NO_INHERITANCE,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_ALIAS,
				TrusteeValue: windows.TrusteeValueFromSID(usersSID),
			},
		},
	}, nil)
	if err != nil {
		t.Fatalf("ACLFromEntries(usersRead): %v", err)
	}
	if err := inspectFileDACL(usersReadACL, true, false); err == nil {
		t.Fatalf("expected Users:R to be rejected under strict secret check")
	}
	if err := inspectFileDACL(usersReadACL, false, false); err != nil {
		t.Fatalf("expected Users:R (non-write) to be allowed under writable-safety check for upgrade migration: %v", err)
	}

	usersWriteACL, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{
		{
			AccessPermissions: windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE,
			AccessMode:        windows.SET_ACCESS,
			Inheritance:       windows.NO_INHERITANCE,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_ALIAS,
				TrusteeValue: windows.TrusteeValueFromSID(usersSID),
			},
		},
	}, nil)
	if err != nil {
		t.Fatalf("ACLFromEntries(usersWrite): %v", err)
	}
	if err := inspectFileDACL(usersWriteACL, false, false); err == nil {
		t.Fatalf("expected Users:W to be rejected under writable-safety check")
	}
}

func TestHardenAndVerifySecretFileACL_LiveWhenElevated(t *testing.T) {
	if !IsElevated() {
		t.Skip("skipping live SYSTEM/Administrators ACL hardening test on non-elevated runner")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "secret.json")
	if err := os.WriteFile(p, []byte(`{"authToken":"x"}`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := HardenSecretFileACL(p); err != nil {
		t.Fatalf("HardenSecretFileACL: %v", err)
	}
	sd, err := windows.GetNamedSecurityInfo(p, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("GetNamedSecurityInfo: %v", err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatalf("DACL: %v", err)
	}
	if err := inspectFileDACL(dacl, true, false); err != nil {
		t.Fatalf("hardened DACL failed strict inspection: %v", err)
	}
}

// HardenDataDirACL precisa tornar CONFIÁVEL um diretório legado com Users:(M)
// herdado — era exatamente o ACE que fazia o debug_config.json ser rejeitado
// como "diretorio pai nao confiavel" e quarentenado a cada boot.
func TestHardenDataDirACL_LiveWhenElevated(t *testing.T) {
	if !IsElevated() {
		t.Skip("skipping live data-dir ACL hardening test on non-elevated runner")
	}

	if err := HardenDataDirACL(""); err == nil {
		t.Fatal("diretório vazio precisa ser rejeitado")
	}
	if err := HardenDataDirACL(filepath.Join(t.TempDir(), "nao-existe")); err == nil {
		t.Fatal("diretório inexistente precisa ser rejeitado")
	}

	dir := filepath.Join(t.TempDir(), "Discovery")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Simula o legado: Users com escrita herdada no diretório de dados.
	legacy := exec.Command("icacls.exe", dir, "/grant", "*S-1-5-32-545:(M)", "/Q")
	if out, err := legacy.CombinedOutput(); err != nil {
		t.Fatalf("simular legado (icacls): %v — %s", err, strings.TrimSpace(string(out)))
	}
	if err := verifyTrustedDirectoryWriteACL(dir, false); err == nil {
		t.Fatal("pré-condição: diretório com Users:(M) deveria ser rejeitado")
	}

	if err := HardenDataDirACL(dir); err != nil {
		t.Fatalf("HardenDataDirACL: %v", err)
	}
	if err := verifyTrustedDirectoryWriteACL(dir, false); err != nil {
		t.Fatalf("diretório continua nao confiavel depois do hardening: %v", err)
	}
}
