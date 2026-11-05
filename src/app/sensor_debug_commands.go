package app

// ── Painel de debug de sensores (bridge HTTP de debug) ─────────────────────
//
// O servidor de debug (app/debug_http.go) expõe AUTOMATICAMENTE todo método
// exportado de *App em /api/<MethodName>, por reflexão. Por isso NÃO existe
// rota declarada aqui: basta o método ser exportado, ter 0 ou 1 parâmetro
// (fora context.Context) e devolver (result, error) — o erro non-nil vira
// HTTP 500 com {"error": msg}.
//
// Os métodos abaixo são o "painel de debug" do motor de sensores: o operador
// enxerga o estado do motor, dispara uma coleta agora e força a releitura da
// policy logo após editar um sensor no console.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"discovery/app/core/automation"
)

// sensorDebugJSONMaxBytes limita o corpo aceito em idsJSON: é uma entrada de
// painel de debug, não um canal de dados — um corpo gigante só serviria para
// ocupar memória à toa.
const sensorDebugJSONMaxBytes = 1 << 20

// sensorDebugRunRequest é a forma OBJETO aceita em idsJSON:
//
//	{"ids":["guid1"],"forceSync":true}
type sensorDebugRunRequest struct {
	IDs       []string `json:"ids"`
	ForceSync bool     `json:"forceSync"`
}

// parseSensorDebugRunPayload interpreta o parâmetro único de RunSensorsNowDebug.
//
// O bridge HTTP desembrulha as quotes do JSON string (unmarshalAPIParam), então
// o que chega aqui costuma ser o TEXTO interno. Formas toleradas (com trim):
//
//	""                              -> todos os sensores, sem forceSync
//	"[]"                            -> idem
//	"[\"guid1\",\"guid2\"]"          -> apenas esses ids
//	"{\"ids\":[\"guid1\"],\"forceSync\":true}" -> ids + sync da policy
//
// Um objeto {"ids":null} é intencional (todos, sem sync), então NÃO é erro.
// Qualquer outra coisa (texto solto, número, array com itens não-string) vira
// erro em PT-BR — nada de pânico: a entrada vem do painel/operador.
func parseSensorDebugRunPayload(idsJSON string) ([]string, bool, error) {
	trimmed := strings.TrimSpace(idsJSON)
	if trimmed == "" {
		return nil, false, nil
	}
	if len(trimmed) > sensorDebugJSONMaxBytes {
		return nil, false, fmt.Errorf("parametro idsJSON grande demais (%d bytes, maximo %d)", len(trimmed), sensorDebugJSONMaxBytes)
	}

	switch trimmed[0] {
	case '[':
		var ids []string
		if err := json.Unmarshal([]byte(trimmed), &ids); err != nil {
			return nil, false, fmt.Errorf("idsJSON invalido: esperado array JSON de ids (ex.: [\"guid1\",\"guid2\"]): %v", err)
		}
		return normalizedSensorIDs(ids), false, nil
	case '{':
		var req sensorDebugRunRequest
		if err := json.Unmarshal([]byte(trimmed), &req); err != nil {
			return nil, false, fmt.Errorf("idsJSON invalido: esperado objeto JSON (ex.: {\"ids\":[\"guid1\"],\"forceSync\":true}): %v", err)
		}
		return normalizedSensorIDs(req.IDs), req.ForceSync, nil
	default:
		return nil, false, fmt.Errorf("idsJSON invalido: use \"\", \"[]\", um array de ids ou {\"ids\":[...],\"forceSync\":bool}; recebido %q", sensorDebugPreview(trimmed))
	}
}

// normalizedSensorIDs limpa a lista de ids: descarta espaços e entradas vazias
// (um id vazio no array é ruído do painel, não um pedido de "todos").
func normalizedSensorIDs(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if trimmed := strings.TrimSpace(id); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// sensorDebugPreview encurta a entrada devolvida na mensagem de erro.
func sensorDebugPreview(raw string) string {
	const max = 120
	if len(raw) > max {
		return raw[:max] + "..."
	}
	return raw
}

// sensorDebugContext devolve o contexto do App, com fallback para
// context.Background() quando o agente ainda não publicou o seu (ou quando o
// App foi montado por teste) — o RunSensorsNow só usa o ctx para o sync da
// policy, então um contexto de fundo é um fallback seguro.
func (a *App) sensorDebugContext() context.Context {
	if a != nil && a.ctx != nil {
		return a.ctx
	}
	return context.Background()
}

// sensorDebugService devolve o serviço de automação para o painel de debug de
// forma RECUPERÁVEL.
//
// O AutomationSvc é criado no boot da App (app.go: "a.AutomationSvc =
// automation.NewService(...)"), NÃO por reflexão, então este caminho não o
// recria: fabricar um Service aqui daria ao painel um motor paralelo, sem DB,
// sem package manager e sem a factory de config real — pior que um erro claro,
// porque ele responderia "0 sensores" como se fosse o motor de verdade.
//
// O que ele faz é não transformar uma corrida de boot em erro fatal:
//   - Serviço presente: usa o motor real.
//   - Serviço ausente: devolve erro PT-BR explícito (a UI mostra
//     debug.sensorsRunError) dizendo que a automação ainda está subindo e
//     pedindo um RefreshSensorPolicyDebug depois — em vez do genérico
//     "servico de automacao indisponivel", que não diz ao operador o que fazer.
func (a *App) sensorDebugService() (*automation.Service, error) {
	if a == nil || a.AutomationSvc == nil {
		return nil, fmt.Errorf("motor de sensores ainda nao esta pronto (automacao em inicializacao); aguarde alguns segundos e use `Atualizar policy`")
	}
	return a.AutomationSvc, nil
}

// sensorDebugContainerCount informa quantos sensores a policy local conhece,
// para a mensagem dizer POR QUE nada foi agendado (policy ainda não sincronizada
// vs. ids que não existem). Devolve -1 quando a contagem não está disponível
// (serviço ausente ou ponteiro nulo) — nunca inventa zero, que seria lido como
// "policy vazia".
func sensorDebugContainerCount(svc *automation.Service) int {
	if svc == nil {
		return -1
	}
	snapshot := svc.SensorDebugSnapshot()
	return len(snapshot.Sensors)
}

// sensorDebugRunMessage monta a mensagem PT-BR do run-now, incluindo o motivo
// quando NADA foi agendado:
//   - a policy local está vazia (motor ainda não sincronizou);
//   - ou os ids pedidos não existem na policy local.
//
// Agendar zero é resultado VÁLIDO (não é erro): quem decide a mensagem é esta
// camada, que é quem tem o snapshot para explicar o que aconteceu.
func sensorDebugRunMessage(count, requested, known int) string {
	if count > 0 {
		return sensorDebugScheduledMessage(count, requested)
	}
	if requested > 0 {
		return fmt.Sprintf("nenhum sensor agendado: nenhum dos %d id(s) existe na policy local (0 de %d id(s) encontrados na policy local)", requested, requested)
	}
	if known == 0 {
		return "nenhum sensor agendado: a policy local de sensores esta vazia (aguarde o primeiro sync ou use `Atualizar policy`)"
	}
	return fmt.Sprintf("nenhum sensor agendado: os %d sensor(es) da policy local ja estao em dia", known)
}

// sensorDebugEmptySnapshot monta um snapshot VAZIO e VÁLIDO: o painel precisa
// renderizar sempre, mesmo com o serviço de automação indisponível.
func sensorDebugEmptySnapshot(debugMode bool, lastError string) automation.SensorDebugSnapshot {
	return automation.SensorDebugSnapshot{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		DebugMode:   debugMode,
		Engine:      automation.SensorEngineStatus{LastError: lastError},
		// Sensores sempre não-nil: o painel não deve tratar null.
		Sensors: []automation.SensorDebugView{},
	}
}

// SensorDebugSnapshotCore monta o snapshot a partir do MOTOR DESTE PROCESSO
// (o serviço), sem tentar o RPC. É o que o lado serviço expõe em
// "sensors:debug_snapshot" e o que o painel usa em standalone.
//
// Nunca devolve erro: o painel precisa renderizar sempre. Quando o motor não
// está disponível devolve snapshot vazio e VÁLIDO com o motivo em
// Engine.LastError.
func (a *App) SensorDebugSnapshotCore() automation.SensorDebugSnapshot {
	if a == nil {
		return sensorDebugEmptySnapshot(false, sensorDebugCoreUnavailable)
	}
	debugMode := a.RuntimeFlags.DebugMode
	if a.AutomationSvc == nil {
		return sensorDebugEmptySnapshot(debugMode, sensorDebugCoreUnavailable)
	}
	snapshot := a.AutomationSvc.SensorDebugSnapshot()
	snapshot.DebugMode = debugMode
	return snapshot
}

// sensorDebugSnapshotView é o envelope que o painel recebe. Ele embute o
// snapshot do núcleo e acrescenta a ORIGEM do dado.
//
// Por que um envelope em vez de um campo dentro de SensorDebugSnapshot: o tipo
// do núcleo vive em app/core/automation, que está FORA do escopo desta task.
// O envelope mantém o contrato existente intacto (o frontend lê os mesmos
// campos, agora um nível abaixo) e não obriga a mudar o núcleo.
type sensorDebugSnapshotView struct {
	automation.SensorDebugSnapshot
	// FromService diz se o retrato veio do motor do SERVIÇO (true) ou do
	// motor local/standalone (false). Sem isso o operador não distingue
	// "policy vazia de verdade" de "estou olhando o processo errado".
	FromService bool `json:"fromService"`
	// Origin é o rótulo legível da origem ("servico"/"local").
	Origin string `json:"origin,omitempty"`
}

// sensorDebugSnapshotView monta o envelope com a origem do dado.
func (a *App) sensorDebugSnapshotView(snapshot automation.SensorDebugSnapshot, origin string) sensorDebugSnapshotView {
	// O painel lê GeneratedAt para saber a IDADE do retrato. Um zero-value
	// passaria como "nunca gerado" mesmo com o motor respondendo, então
	// preenchemos quando o núcleo não trouxe (contrato: o campo é obrigatório).
	if strings.TrimSpace(snapshot.GeneratedAt) == "" {
		snapshot.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	}
	return sensorDebugSnapshotView{
		SensorDebugSnapshot: snapshot,
		FromService:         origin == sensorDebugOriginService,
		Origin:              origin,
	}
}
// sensorDebugIPCRequest é o ponto de indireção do RPC do painel.
//
// Existe SÓ para teste: ipcRequest exige um *IPCClient ligado a um named pipe
// real, o que torna o caminho companion inalcançável num teste unitário. Com
// nil (produção) delega ao ipcRequest normal — custo zero, comportamento igual.
var sensorDebugIPCRequest = func(a *App, method string, payload map[string]any) (map[string]any, bool) {
	return a.ipcRequest(method, payload)
}
// Rótulos da origem do dado. São constantes para o lado serviço e a UI não
// divergirem do texto que o teste verifica.
const (
	sensorDebugCoreUnavailable = "motor de sensores ainda nao esta pronto (automacao em inicializacao)"
	sensorDebugOriginService   = "servico"
	sensorDebugOriginLocal     = "local"
)

// GetSensorDebugSnapshot devolve o snapshot do motor de sensores para o painel
// de debug (GET /api/GetSensorDebugSnapshot).
//
// Com o serviço de automação indisponível NÃO devolve erro: devolve um snapshot
// vazio, porém válido e legível, com o motivo em Engine.LastError — é o painel
// que o operador abre justamente quando algo está errado, e um 500 o deixaria
// sem nenhuma informação.
func (a *App) GetSensorDebugSnapshot() (any, error) {
	if a == nil {
		return a.sensorDebugSnapshotView(sensorDebugEmptySnapshot(false, "servico de automacao indisponivel"), sensorDebugOriginLocal), nil
	}
	// CAUSA RAIZ (M5): a UI é companion e NÃO roda o loop de sensores — o
	// AutomationSvc local existe mas nunca coleta. O motor de verdade roda no
	// serviço, então o RPC vem PRIMEIRO, exatamente como GetAutomationState
	// (automation.go:12-25). Sem ele o painel ficava permanentemente vazio
	// enquanto o serviço coletava normalmente.
	if snapshot, ok := a.getSensorDebugSnapshotCompanion(); ok {
		// NÃO sobrescrevemos snapshot.Engine.LastError: o serviço pode tê-lo
		// preenchido por um motivo legítimo (backoff de sync, falha de
		// entrega). Trocá-lo por um texto local mostraria erro onde não há.
		return a.sensorDebugSnapshotView(snapshot, sensorDebugOriginService), nil
	}
	// Standalone (ou RPC não respondeu): motor local.
	return a.sensorDebugSnapshotView(a.SensorDebugSnapshotCore(), sensorDebugOriginLocal), nil
}

// sensorDebugCompanionRefusal devolve uma recusa PT-BR quando o painel roda em
// modo companion e a ação pedida é MUTANTE.
//
// Por que recusar em vez de tentar: em companion a UI não roda o loop de
// sensores (M5). Chamar o AutomationSvc local "funciona" — devolve uma
// contagem e uma mensagem de sucesso — mas NÃO coleta nada, porque quem coleta
// é o serviço. Isso é pior que um erro: o operador clica em "Coletar agora",
// lê "coleta agendada" e conclui que a coleta foi agendada.
//
// Roteá-la por RPC exigiria um método mutante novo, que precisa de entrada
// explícita em ipcKnownMutatingMethods (ipc_auth.go) para exigir canal admin +
// token — decisão adiada pelo Lead para uma rodada própria. Até lá, o painel
// diz a verdade em vez de fingir.
//
// Devolve (mensagem, true) quando deve recusar; ("", false) quando pode seguir
// no caminho local normal.
func (a *App) sensorDebugCompanionRefusal(acao string) (string, bool) {
	if a == nil || a.ipcClient == nil {
		return "", false
	}
	return fmt.Sprintf(
		"%s roda no servico DiscoveryAgent, nao nesta janela. Use o console para disparar agora ou aguarde o proximo ciclo do motor — a coleta continua acontecendo normalmente no servico.",
		acao), true
}
// RunSensorsNowDebug agenda a coleta imediata de sensores a partir do painel de
// debug (POST /api/RunSensorsNowDebug).
//
// O parâmetro único é uma STRING (o bridge desembrulha as quotes do JSON):
// aceita "", "[]", um array de ids ou {"ids":[...],"forceSync":bool} — ver
// parseSensorDebugRunPayload. Assim como o comando "runsensors"
// (agent_commands_sensors.go), apenas AGENDA: o motor roda no loop de sensores.
// forceSync=true sincroniza a policy de sensores ANTES de agendar, que é o que
// o operador quer logo depois de editar um sensor no console.
func (a *App) RunSensorsNowDebug(idsJSON string) (any, error) {
	// Em companion o motor que coleta é o do SERVIÇO. Não adianta marcar o
	// motor local: ele nunca roda (M5). Dizemos isso ao operador em vez de
	// devolver uma mensagem de sucesso vazia.
	if msg, recusar := a.sensorDebugCompanionRefusal("a coleta imediata"); recusar {
		if a != nil {
			a.Logs.Append("[sensores] debug: run-now recusado em modo companion (o motor roda no servico)")
		}
		return map[string]any{"scheduled": 0, "message": msg, "routedToService": false}, nil
	}
	svc, err := a.sensorDebugService()
	if err != nil {
		if a != nil {
			a.Logs.Append(fmt.Sprintf("[sensores] debug: run-now indisponivel: %v", err))
		}
		return nil, err
	}
	ids, forceSync, err := parseSensorDebugRunPayload(idsJSON)
	if err != nil {
		a.Logs.Append(fmt.Sprintf("[sensores] debug: parametro rejeitado: %v", err))
		return nil, err
	}
	a.Logs.Append(fmt.Sprintf("[sensores] debug: run-now recebido: sensores=%d forceSync=%t", len(ids), forceSync))

	known := sensorDebugContainerCount(svc)
	count, err := svc.RunSensorsNow(a.sensorDebugContext(), ids, forceSync)
	if err != nil {
		// Só indisponibilidade real chega aqui: agendar zero é sucesso.
		a.Logs.Append(fmt.Sprintf("[sensores] debug: run-now sem coleta: %v", err))
		return nil, err
	}
	message := sensorDebugRunMessage(count, len(ids), known)
	a.Logs.Append("[sensores] debug: " + strings.TrimSpace(message))
	return map[string]any{"scheduled": count, "message": message}, nil
}

// RefreshSensorPolicyDebug relê a policy de sensores e reagenda TODOS eles
// (POST /api/RefreshSensorPolicyDebug). É o "aplicar agora" do operador: ele
// edita o sensor no console e quer ver o efeito sem esperar o próximo ciclo.
//
// ids=nil agenda todos; forceSync=true garante o sync da policy antes de marcar
// os sensores como devidos.
func (a *App) RefreshSensorPolicyDebug() (any, error) {
	// Mesmo raciocínio do run-now: em companion o sync de policy roda no
	// serviço. Recusar com clareza é melhor que "sincronizada" sem efeito.
	if msg, recusar := a.sensorDebugCompanionRefusal("a ressincronizacao de policy"); recusar {
		if a != nil {
			a.Logs.Append("[sensores] debug: refresh de policy recusado em modo companion (o motor roda no servico)")
		}
		return map[string]any{"scheduled": 0, "message": msg, "routedToService": false}, nil
	}
	svc, err := a.sensorDebugService()
	if err != nil {
		if a != nil {
			a.Logs.Append(fmt.Sprintf("[sensores] debug: refresh de policy indisponivel: %v", err))
		}
		return nil, err
	}
	a.Logs.Append("[sensores] debug: refresh de policy solicitado (todos os sensores, forceSync=true)")

	count, err := svc.RunSensorsNow(a.sensorDebugContext(), nil, true)
	if err != nil {
		a.Logs.Append(fmt.Sprintf("[sensores] debug: refresh de policy sem coleta: %v", err))
		return nil, err
	}
	message := fmt.Sprintf("policy de sensores sincronizada e coleta agendada para todos os %d sensor(es)", count)
	a.Logs.Append("[sensores] debug: " + strings.TrimSpace(message))
	return map[string]any{"scheduled": count, "message": message}, nil
}

// sensorDebugScheduledMessage monta a mensagem PT-BR no mesmo espírito de
// agent_commands_sensors.go. Quando o operador pediu ids específicos e alguns
// não existem na policy local, o painel diz explicitamente quantos foram
// encontrados — senão o console acharia que todos rodaram.
func sensorDebugScheduledMessage(count, requested int) string {
	message := fmt.Sprintf("coleta agendada para %d sensor(es)", count)
	if requested > 0 && count < requested {
		message = fmt.Sprintf("%s (%d de %d id(s) encontrados na policy local)", message, count, requested)
	}
	return message
}
