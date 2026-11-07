//go:build windows

package sysctrl

// ─────────────────────────────────────────────────────────────────────────────
// EXECUÇÃO GUARDADA DE AÇÃO EM SERVIÇO (F7)
//
// Esta é a única porta pela qual o canal servidor→agente deve executar
// start/stop/restart. Ela SEMPRE avalia o guard ANTES de tocar no SCM: não
// existe caminho que chame StartService/StopService/RestartService sem passar
// por EvaluateServiceAction.
//
// Note que este arquivo é a fronteira entre a decisão pura (service_guard) e o
// efeito real (services_windows). Manter a decisão separada é o que permite
// testá-la exaustivamente sem parar serviço nenhum.
// ─────────────────────────────────────────────────────────────────────────────

// ServiceActionResult é o resultado auditável de uma tentativa.
type ServiceActionResult struct {
	// Executed indica que a ação foi autorizada E aplicada com sucesso.
	Executed bool
	// Decision é a avaliação do guard (sempre presente, inclusive quando recusa).
	Decision ServiceGuardDecision
	// Err é o erro do SCM, quando a ação foi autorizada mas falhou ao aplicar.
	Err error
}

// ApplyServiceAction avalia o guard e, SOMENTE se autorizado, aplica a ação.
//
// productType deve vir do chamador (normalmente DetectProductType()), mas
// pode ser passado explicitamente para permitir teste e para o chamador
// reusar uma leitura já feita.
//
// Fail-closed: qualquer recusa do guard devolve Executed=false SEM tocar no
// SCM. O erro do SCM (quando houver) fica separado da decisão, para que o log
// de auditoria distinga "recusado pela política" de "autorizado mas falhou".
func ApplyServiceAction(serviceName string, action ServiceAction, productType string) ServiceActionResult {
	decision := EvaluateServiceAction(DefaultServiceGuardPolicy(), serviceName, action, productType)
	result := ServiceActionResult{Decision: decision}

	if !decision.Allowed {
		// Barreira de segurança: NÃO executar. Retorna imediatamente.
		return result
	}

	var err error
	switch action {
	case ActionStart:
		err = StartService(decision.ServiceName)
	case ActionStop:
		err = StopService(decision.ServiceName)
	case ActionRestart:
		err = RestartService(decision.ServiceName)
	default:
		// Inalcançável: EvaluateServiceAction já recusa ação inválida.
		err = errUnreachableAction
	}

	if err != nil {
		result.Err = err
		return result
	}
	result.Executed = true
	return result
}

// errUnreachableAction cobre o caso (impossível hoje) de uma ação que passou
// pelo guard mas não tem case de execução. Existe para o switch ser exaustivo:
// se alguém adicionar uma ação nova ao guard e esquecer aqui, o erro é
// explícito em vez de um "sucesso" silencioso.
var errUnreachableAction = errUnreachable("acao autorizada sem executor implementado")

type errUnreachable string

func (e errUnreachable) Error() string { return string(e) }
