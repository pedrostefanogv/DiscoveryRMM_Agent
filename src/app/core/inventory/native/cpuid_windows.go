//go:build windows

package native

import (
	"strings"

	cpuid "github.com/klauspost/cpuid/v2"

	"discovery/app/core/models"
)

// collectCPUFeaturesNative returns CPU feature flags using the cpuid library
// (native CPUID instruction), without any subprocess.
func collectCPUFeaturesNative() []models.CPUFeature {
	var features []models.CPUFeature

	// Feature flags (boolean).
	flagNames := []struct {
		flag cpuid.FeatureID
		name string
	}{
		{cpuid.SSE, "sse"},
		{cpuid.SSE2, "sse2"},
		{cpuid.SSE3, "sse3"},
		{cpuid.SSSE3, "ssse3"},
		{cpuid.SSE4, "sse4"},
		{cpuid.SSE42, "sse4_2"},
		{cpuid.AESNI, "aes"},
		{cpuid.AVX, "avx"},
		{cpuid.AVX2, "avx2"},
		{cpuid.AVX512F, "avx512f"},
		{cpuid.FMA3, "fma3"},
		{cpuid.BMI1, "bmi1"},
		{cpuid.BMI2, "bmi2"},
		{cpuid.VMX, "vmx"},
		{cpuid.SVM, "svm"},
		{cpuid.NX, "nx"},
		{cpuid.SYSCALL, "syscall"},
		{cpuid.RDRAND, "rdrand"},
		{cpuid.RDSEED, "rdseed"},
		{cpuid.SHA, "sha"},
		{cpuid.ADX, "adx"},
		{cpuid.SGX, "sgx"},
	}

	for _, f := range flagNames {
		value := "0"
		if cpuid.CPU.Has(f.flag) {
			value = "1"
		}
		features = append(features, models.CPUFeature{
			Feature: f.name,
			Value:   value,
		})
	}

	// Vendor and brand.
	if cpuid.CPU.VendorString != "" {
		features = append(features, models.CPUFeature{
			Feature: "vendor",
			Value:   strings.TrimSpace(cpuid.CPU.VendorString),
		})
	}
	if cpuid.CPU.BrandName != "" {
		features = append(features, models.CPUFeature{
			Feature: "brand",
			Value:   strings.TrimSpace(cpuid.CPU.BrandName),
		})
	}

	return features
}
