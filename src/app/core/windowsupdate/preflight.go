package windowsupdate

import "errors"

// Preflight antes de qualquer mutacao (WU-06, decisao D07).
//
// Instalar patch sem checar condicoes basicas e o caminho mais curto para
// incidente: download que enche o disco, instalacao em notebook na bateria que
// desliga no meio do servicing, ou tentativa com o servico do Windows Update
// parado (que falha com erro obscuro). O preflight roda ANTES de download e
// instalacao e devolve um motivo estavel, nao um texto livre.
const (
	// PreflightCodeServiceStopped: servico wuauserv parado/desabilitado.
	PreflightCodeServiceStopped = "service_stopped"
	// PreflightCodeDiskSpace: espaco livre insuficiente para o download.
	PreflightCodeDiskSpace = "disk_space"
	// PreflightCodeOnBattery: maquina na bateria e a politica bloqueia.
	PreflightCodeOnBattery = "on_battery"
)

// PreflightFailure e o motivo estruturado de um preflight que barrou a acao.
type PreflightFailure struct {
	Code    string
	Message string
}

func (f *PreflightFailure) Error() string {
	if f == nil {
		return ""
	}
	return "preflight " + f.Code + ": " + f.Message
}

// PreflightInput agrega os sinais coletados na maquina.
type PreflightInput struct {
	// ServiceState: running | stopped | disabled | unknown.
	ServiceState string
	// FreeDiskBytes: espaco livre na unidade do sistema.
	FreeDiskBytes int64
	// RequiredDiskBytes: espaco minimo exigido (0 = nao checar).
	RequiredDiskBytes int64
	// OnBattery: maquina esta sem AC.
	OnBattery bool
	// BlockOnBattery: bloqueia na bateria (false = apenas avisa).
	BlockOnBattery bool
}

// EvaluatePreflight devolve o primeiro motivo que barra a instalacao, ou nil.
//
// Ordem importa: servico parado e o erro mais comum e o mais barato de
// detectar; disco vem antes de bateria porque encher o disco e irreversivel
// para o cliente.
func EvaluatePreflight(input PreflightInput) *PreflightFailure {
	switch input.ServiceState {
	case "stopped", "disabled":
		return &PreflightFailure{
			Code:    PreflightCodeServiceStopped,
			Message: "servico do Windows Update " + input.ServiceState,
		}
	}

	if input.RequiredDiskBytes > 0 && input.FreeDiskBytes < input.RequiredDiskBytes {
		return &PreflightFailure{
			Code:    PreflightCodeDiskSpace,
			Message: "espaco livre insuficiente para o download",
		}
	}

	if input.BlockOnBattery && input.OnBattery {
		return &PreflightFailure{
			Code:    PreflightCodeOnBattery,
			Message: "maquina na bateria e a politica exige energia AC",
		}
	}

	return nil
}

// RunPreflight coleta os sinais da maquina e avalia o preflight. Devolve a
// falha (nil = pode prosseguir) e se a maquina esta na bateria, para o chamador
// registrar o aviso quando a politica apenas avisa.
func RunPreflight(requiredDiskBytes int64, blockOnBattery bool) (*PreflightFailure, bool) {
	input := gatherPreflightInput(requiredDiskBytes, blockOnBattery)
	return EvaluatePreflight(input), input.OnBattery
}

// ErrorCode mapeia um erro do pacote para o codigo estavel usado no output do
// comando e na classificacao de falha da API.
func ErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var preflight *PreflightFailure
	if errors.As(err, &preflight) {
		return preflight.Code
	}
	switch {
	case errors.Is(err, ErrServiceDisabled):
		return "service_disabled"
	case errors.Is(err, ErrWuaUnavailable):
		return "wua_unavailable"
	case errors.Is(err, ErrTimeout):
		return "timeout"
	case errors.Is(err, ErrBusy):
		return "busy"
	case errors.Is(err, ErrUnsupportedOS):
		return "unsupported"
	default:
		return "install_failed"
	}
}
