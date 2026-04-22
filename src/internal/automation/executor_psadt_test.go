package automation

import "testing"

func TestNormalizePSADTExecutionResult(t *testing.T) {
	policy := normalizePSADTPolicy(PSADTPolicy{})
	res := normalizePSADTExecutionResult(ExecutionResult{Success: false, ExitCode: 3010, ExitCodeSet: true, ErrorMessage: "reboot"}, policy)
	if !res.Success {
		t.Fatalf("expected 3010 to be normalized as success")
	}
	if res.ErrorMessage != "" {
		t.Fatalf("expected error message to be cleared on reboot-success")
	}

	res = normalizePSADTExecutionResult(ExecutionResult{Success: false, ExitCode: 60008, ExitCodeSet: true, ErrorMessage: "import fail"}, policy)
	if res.Success {
		t.Fatalf("expected 60008 to remain failure")
	}
}

func TestShouldFallbackFromPSADT(t *testing.T) {
	policy := normalizePSADTPolicy(PSADTPolicy{})
	cases := []struct {
		code int
		want bool
	}{
		{0, false},
		{3010, false},
		{60001, true},
		{60008, true},
		{70010, true},
		{127, true},
		{42, true},
		{1602, false},
	}

	for _, tc := range cases {
		got := shouldFallbackFromPSADT(classifyPSADTExitCodeWithPolicy(tc.code, policy), policy)
		if got != tc.want {
			t.Fatalf("code=%d expected %t got %t", tc.code, tc.want, got)
		}
	}
}

func TestClassifyPSADTExitCode(t *testing.T) {
	if got := classifyPSADTExitCode(0); got != psadtExitSuccess {
		t.Fatalf("expected success, got %s", got)
	}
	if got := classifyPSADTExitCode(3010); got != psadtExitSuccessReboot {
		t.Fatalf("expected success reboot, got %s", got)
	}
	if got := classifyPSADTExitCode(1602); got != psadtExitUserDenied {
		t.Fatalf("expected user denied, got %s", got)
	}
	if got := classifyPSADTExitCode(60008); got != psadtExitRecoverable {
		t.Fatalf("expected recoverable, got %s", got)
	}
}

func TestClassifyPSADTExitCodeWithPolicy_CustomSuccessAndUnknownFatal(t *testing.T) {
	policy := normalizePSADTPolicy(PSADTPolicy{
		SuccessExitCodes:      []int{0, 42},
		UnknownExitCodePolicy: "fatal_failure",
	})

	if got := classifyPSADTExitCodeWithPolicy(42, policy); got != psadtExitSuccess {
		t.Fatalf("expected policy custom success for exit code 42, got %s", got)
	}
	if got := classifyPSADTExitCodeWithPolicy(9999, policy); got != psadtExitFatal {
		t.Fatalf("expected unknown exit code to be fatal by policy, got %s", got)
	}
}

func TestShouldFallbackFromPSADT_DisabledByPolicy(t *testing.T) {
	policy := normalizePSADTPolicy(PSADTPolicy{FallbackPolicy: "no_fallback"})
	if shouldFallbackFromPSADT(psadtExitRecoverable, policy) {
		t.Fatalf("expected fallback to be disabled by policy")
	}
}

func TestMSIPackageDetectionAndNormalization(t *testing.T) {
	if !isMSIPackageID("C:/tmp/app.msi") {
		t.Fatalf("expected .msi path to be detected as MSI")
	}
	if !isMSIPackageID("msi:C:/tmp/app.msi") {
		t.Fatalf("expected msi: prefix to be detected as MSI")
	}
	if isMSIPackageID("Microsoft.PowerToys") {
		t.Fatalf("expected winget id to not be detected as MSI")
	}

	if got := normalizeMSIPackagePath("msi:C:/tmp/app.msi"); got != "C:/tmp/app.msi" {
		t.Fatalf("expected normalized MSI path without prefix, got %q", got)
	}
}
