//go:build windows

package platform

import (
	"os"
	"path/filepath"
	"runtime"
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
