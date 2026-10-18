package selfupdate

import (
	"fmt"
	"strings"
)

// installerLaunchMode é o caminho de criação do processo do instalador NSIS.
//
// O modo SERVIÇO (sessão 0, token SYSTEM) NUNCA pode usar ShellExecuteEx
// ("runas"): não existe desktop interativo para o UAC e a chamada falha (ou,
// pior, fica pendurada esperando um consentimento que ninguém vê). Nesse
// contexto o instalador é criado por CreateProcess, herdando o token SYSTEM.
type installerLaunchMode int

const (
	// installerLaunchShellElevated usa ShellExecuteEx("runas") — só para a UI
	// (processo interativo, NÃO elevado) pedir o UAC ao usuário.
	installerLaunchShellElevated installerLaunchMode = iota
	// installerLaunchCreateProcess usa CreateProcess direto, sem UAC. Serve
	// para processos já elevados/SYSTEM (serviço, task elevada) e mantém o
	// instalador independente do job object do agente.
	installerLaunchCreateProcess
	// installerLaunchUnsupported é o modo serviço SEM token elevado: não há
	// como instalar (nem por runas, nem por CreateProcess) — erro claro.
	installerLaunchUnsupported
)

// decideInstallerLaunchMode é a decisão PURA (testável sem Windows) entre os
// caminhos de lançamento do instalador:
//
//   - modo serviço + elevado   → CreateProcess (SYSTEM cria o instalador);
//   - modo serviço + não elevado → unsupported (nunca runas na sessão 0);
//   - UI elevada               → CreateProcess (independente do job object);
//   - UI comum (não elevada)   → ShellExecuteEx("runas") com UAC.
func decideInstallerLaunchMode(serviceMode, elevated bool) installerLaunchMode {
	if serviceMode {
		if elevated {
			return installerLaunchCreateProcess
		}
		return installerLaunchUnsupported
	}
	if elevated {
		return installerLaunchCreateProcess
	}
	return installerLaunchShellElevated
}

// installerElevationAllowed é a trava de segurança do ShellExecuteEx("runas")
// para QUALQUER chamador (self-update, automação/PSADT, descomissionamento):
// na sessão 0/SYSTEM não existe desktop interativo, então solicitar elevação
// não tem como ser atendido — pode falhar ou ficar pendurado até o timeout.
// A decisão é pura para ser testável sem Windows.
func installerElevationAllowed(serviceMode bool) error {
	if serviceMode {
		return fmt.Errorf("ShellExecuteEx(runas) bloqueado: processo em modo serviço (sessão 0) não tem desktop/UAC; use CreateProcess como SYSTEM")
	}
	return nil
}

// hasServiceModeArg reconhece --service / /service / -service (o binário do
// serviço também pode rodar manualmente fora do SCM, em debug). É pura para
// ser testável em qualquer SO.
func hasServiceModeArg(args []string) bool {
	for _, raw := range args {
		arg := strings.ToLower(strings.TrimSpace(raw))
		if arg == "" {
			continue
		}
		if strings.TrimLeft(arg, "-/") == "service" {
			return true
		}
	}
	return false
}
