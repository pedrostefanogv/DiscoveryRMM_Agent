package automation

import "testing"

func TestBuildPSADTWingetArguments(t *testing.T) {
	args, err := buildPSADTWingetArguments("Microsoft.PowerToys", "install")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if args == "" {
		t.Fatalf("expected non-empty args")
	}

	_, err = buildPSADTWingetArguments("", "install")
	if err == nil {
		t.Fatalf("expected error for empty package id")
	}

	_, err = buildPSADTWingetArguments("pkg", "invalid")
	if err == nil {
		t.Fatalf("expected error for invalid operation")
	}
}

func TestNormalizePSADTExecutionResult(t *testing.T) {
	res := normalizePSADTExecutionResult(ExecutionResult{Success: false, ExitCode: 3010, ExitCodeSet: true, ErrorMessage: "reboot"})
	if !res.Success {
		t.Fatalf("expected 3010 to be normalized as success")
	}
	if res.ErrorMessage != "" {
		t.Fatalf("expected error message to be cleared on reboot-success")
	}

	res = normalizePSADTExecutionResult(ExecutionResult{Success: false, ExitCode: 60008, ExitCodeSet: true, ErrorMessage: "import fail"})
	if res.Success {
		t.Fatalf("expected 60008 to remain failure")
	}
}

func TestShouldFallbackFromPSADT(t *testing.T) {
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
		got := shouldFallbackFromPSADT(classifyPSADTExitCode(tc.code))
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
