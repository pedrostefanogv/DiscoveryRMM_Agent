//go:build !windows

package hardwareid

// collect é um stub para plataformas não-Windows.
// TPM via TBS e UUID SMBIOS via WMI são específicos do Windows.
func collect() Info {
	return Info{
		TPMEKError:      "TPM EK suportado apenas em Windows",
		SMBIOSUUIDError: "UUID SMBIOS suportado apenas em Windows",
	}
}
