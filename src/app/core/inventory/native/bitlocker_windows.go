//go:build windows

package native

import (
	"context"

	"discovery/app/core/models"
)

// collectBitLockerNative returns BitLocker volume status via WMI
// (Win32_EncryptableVolume in the root\cimv2\Security\MicrosoftVolumeEncryption
// namespace).
func collectBitLockerNative(ctx context.Context) ([]models.BitLockerInfo, error) {
	_ = ctx
	const ns = `root\cimv2\Security\MicrosoftVolumeEncryption`
	rows, err := wmiQuery(ns, "SELECT DriveLetter, ProtectionStatus, ConversionStatus, EncryptionMethod, PercentageEncrypted, LockStatus FROM Win32_EncryptableVolume")
	if err != nil {
		return nil, nil
	}

	items := make([]models.BitLockerInfo, 0, len(rows))
	for _, row := range rows {
		driveLetter := wmiString(row, "DriveLetter")
		if driveLetter == "" {
			continue
		}
		items = append(items, models.BitLockerInfo{
			DriveLetter:         driveLetter,
			ProtectionStatus:    wmiInt(row, "ProtectionStatus"),
			ConversionStatus:    wmiInt(row, "ConversionStatus"),
			EncryptionMethod:    wmiString(row, "EncryptionMethod"),
			PercentageEncrypted: wmiInt(row, "PercentageEncrypted"),
			LockStatus:          wmiInt(row, "LockStatus"),
		})
	}
	return items, nil
}
