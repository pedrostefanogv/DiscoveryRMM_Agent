//go:build !windows

package app

// collectHardwareIdentity é um stub para plataformas não-Windows.
// TPM via TBS e UUID SMBIOS via WMI são específicos do Windows.
func collectHardwareIdentity() HardwareIdentityInfo {
	return HardwareIdentityInfo{
		TPMEKError:      "TPM EK suportado apenas em Windows",
		SMBIOSUUIDError: "UUID SMBIOS suportado apenas em Windows",
	}
}
