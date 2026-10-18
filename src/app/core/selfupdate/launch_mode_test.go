package selfupdate

import "testing"

// A decisão é o contrato de segurança do self-update: no modo serviço (sessão
// 0, SYSTEM) o único caminho permitido é CreateProcess. ShellExecuteEx("runas")
// ali não tem desktop/UAC e pode travar o update para sempre.
func TestDecideInstallerLaunchMode(t *testing.T) {
	cases := []struct {
		name        string
		serviceMode bool
		elevated    bool
		want        installerLaunchMode
	}{
		{"servico-elevado-cria-processo", true, true, installerLaunchCreateProcess},
		{"servico-nao-elevado-nunca-runas", true, false, installerLaunchUnsupported},
		{"ui-elevada-cria-processo", false, true, installerLaunchCreateProcess},
		{"ui-comum-pede-uac", false, false, installerLaunchShellElevated},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decideInstallerLaunchMode(tc.serviceMode, tc.elevated)
			if got != tc.want {
				t.Fatalf("decideInstallerLaunchMode(service=%v, elevated=%v) = %v, esperado %v",
					tc.serviceMode, tc.elevated, got, tc.want)
			}
			if tc.serviceMode && got == installerLaunchShellElevated {
				t.Fatal("modo serviço NUNCA pode escolher ShellExecuteEx(runas)")
			}
		})
	}
}

// Trava de defesa em profundidade: o helper exportado LaunchInstallerElevated
// (usado também por automação/PSADT) não pode chamar "runas" no modo serviço.
func TestInstallerElevationBlockedInServiceMode(t *testing.T) {
	if err := installerElevationAllowed(true); err == nil {
		t.Fatal("modo serviço deve bloquear ShellExecuteEx(runas)")
	}
	if err := installerElevationAllowed(false); err != nil {
		t.Fatalf("UI/serviço elevado pode chamar runas quando não é sessão 0: %v", err)
	}
}

func TestHasServiceModeArg(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"--service"}, true},
		{[]string{"/SERVICE"}, true},
		{[]string{"-service"}, true},
		{[]string{"--startup-minimized", "--service", "--foo"}, true},
		{[]string{"--startup-minimized"}, false},
		{[]string{"--service-mode"}, false},
		{[]string{""}, false},
		{nil, false},
	}
	for _, tc := range cases {
		if got := hasServiceModeArg(tc.args); got != tc.want {
			t.Fatalf("hasServiceModeArg(%v) = %v, esperado %v", tc.args, got, tc.want)
		}
	}
}
