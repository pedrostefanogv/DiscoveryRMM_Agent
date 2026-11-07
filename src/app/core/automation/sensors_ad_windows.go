//go:build windows

package automation

// F5 — Consistência de Active Directory, SEM credencial de domínio.
//
// ─────────────────────────────────────────────────────────────────────────────
// REGRA DE SEGURANÇA (inviolável, vem do plano):
//
//   NUNCA introduzir credencial de domínio, bind LDAP, ou qualquer autenticação
//   contra o AD. Hoje o agente tem ZERO ocorrências de LDAP/credencial de
//   domínio — esse é um estado seguro e ele é PRESERVADO por este arquivo.
//
//   Toda a evidência vem de comandos LOCAIS que rodam como a identidade do
//   agente (SYSTEM no modo serviço) e usam o contexto de máquina do próprio
//   Windows: repadmin, dcdiag e w32tm. Esses utilitários se autenticam pelo
//   contexto do processo — nós não fornecemos usuário, senha nem path de
//   diretório. Nenhum -u/-p/-Credential é passado.
// ─────────────────────────────────────────────────────────────────────────────
//
// ESCOPO: só roda em CONTROLADOR DE DOMÍNIO (ProductType == "LanmanNT"). Em
// servidor membro ou cliente, repadmin/dcdiag não têm o que consultar; nesses
// casos devolvemos coleta vazia SEM erro, que é diferente de falha.
//
// O agente NÃO decide status/severidade: devolve evidência (houve falha?
// quantas? quais mensagens?) e o servidor avalia.
//
// Best-effort em todos os caminhos: comando ausente, timeout ou saída
// inesperada resultam em evidência parcial + motivo, nunca panic.

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"

	"discovery/app/core/sysctrl"
)

// Timeouts dos utilitários de AD. O dcdiag é notoriamente lento (roda uma bateria
// de testes); repadmin/w32tm são rápidos. Estes valores são o teto de cada
// subprocesso — o timeout do sensor continua sendo o limite real da coleta.
const (
	adRepadminTimeout = 30 * time.Second
	adDcdiagTimeout   = 60 * time.Second
	adW32tmTimeout    = 15 * time.Second
)

// Chaves de canal publicadas pelo coletor de consistência de AD.
//
// Estes NOMES são o contrato com o servidor: o servidor deriva os canais a
// partir do mapa Channels (o SensorMeasure não tem campo dedicado para AD, então
// o mapa de canais é o transporte). Renomear qualquer chave quebra o outro lado.
const (
	// ChannelADReplicationFailures: nº de falhas de replicação no /showrepl.
	ChannelADReplicationFailures = "replication_failures"
	// ChannelADReplicationOK: 1 = replicação sem falhas, 0 = há falha.
	ChannelADReplicationOK = "replication_ok"
	// ChannelADDcdiagFailedTests: nº de testes do dcdiag que falharam.
	ChannelADDcdiagFailedTests = "dcdiag_failed_tests"
	// ChannelADDcdiagOK: 1 = nenhum teste falhou, 0 = pelo menos um falhou.
	ChannelADDcdiagOK = "dcdiag_ok"
	// ChannelADTimeOffsetSeconds: desvio de relógio em segundos (>15s quebra Kerberos).
	ChannelADTimeOffsetSeconds = "time_offset_seconds"
	// ChannelADClockOK: 1 = relógio sincronizado dentro da tolerância, 0 = não.
	ChannelADClockOK = "clock_ok"
	// ChannelADCollectOK: 1 = a coleta conseguiu falar com o AD, 0 = não falou.
	// O servidor precisa distinguir "sem falhas" de "não conseguimos perguntar".
	ChannelADCollectOK = "ad_collect_ok"
)

// adConsistencyEvidence é a evidência crua de consistência de AD, já parseada.
//
// Cada bloco é opcional (ponteiro) porque as três coletas são independentes: se
// o dcdiag estourar o timeout mas o repadmin responder, ainda reportamos a
// replicação. Evidência parcial é melhor que nenhuma.
type adConsistencyEvidence struct {
	// ReplicationChecked indica que o repadmin respondeu e pôde ser avaliado.
	ReplicationChecked  bool
	ReplicationFailures int
	// ReplicationMessages são as linhas de falha (limitadas) para o técnico ver
	// o que aconteceu sem abrir chamado às cegas.
	ReplicationMessages []string

	// DcdiagChecked indica que o dcdiag respondeu e pôde ser avaliado.
	DcdiagChecked     bool
	DcdiagFailedTests []string

	// TimeChecked indica que o w32tm respondeu.
	TimeChecked       bool
	TimeOffsetSeconds float64
	TimeSource        string

	// Errors acumula os motivos de cada bloco que não pôde ser coletado. Vazio
	// = tudo coletado.
	Errors []string
}

// Limites de sanidade das mensagens de falha enviadas ao servidor.
const (
	maxADFailureMessages = 10
	maxADMessageLength   = 400
	maxADFailedTests     = 20
)

// ─────────────────────────────────────────────────────────────────────────────
// F5.1 — repadmin /showrepl: falhas de replicação
// ─────────────────────────────────────────────────────────────────────────────

// padroesDeFalhaReplicacao reconhece as linhas que indicam problema na saída do
// "repadmin /showrepl". O repadmin marca falhas com "Error"/"failed" seguidos de
// um código entre parênteses ou dois-pontos; o texto exato varia por versão e
// idioma do SO, então combinamos PADRÕES (não texto localizado).
//
// São PURAS (só regex) para poderem ser testadas sem executar o repadmin.
var padroesDeFalhaReplicacao = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(\d{3,4})\s*(\(|:)\s*`),                // "Error 8524 (0x214c):"
	regexp.MustCompile(`(?i)\b(last\s+(success|failure)\s*:).*fail`), // "Last failure: ..."
	regexp.MustCompile(`(?i)\b(replication|replicacao|replicação)\s+(error|failed|falhou|falha)\b`),
	regexp.MustCompile(`(?i)\b(source|origem)\b.*\b(failed|falhou)\b`),
}

// ignorarNaSaidaReplicacao são linhas que NÃO são falhas, apesar de conterem
// palavras parecidas. Sem esta lista, ""Last success: ..." produziria falso
// positivo e o servidor abriria chamado por um AD saudável.
var ignorarNaSaidaReplicacao = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\blast\s+success\b`),
	regexp.MustCompile(`(?i)\bsuccessfully\b`),
	regexp.MustCompile(`(?i)\bno\s+(replication\s+)?(errors?|falhas?)\b`),
	regexp.MustCompile(`(?i)\b\d+\s+(consecutive\s+)?failures?\s*\b`), // contador de histórico
}

// parseRepadminShowRepl extrai as falhas de replicação da saída de
// "repadmin /showrepl". É PURA.
//
// Devolve a lista de mensagens de falha (deduplicada, truncada e limitada) —
// vazia significa "nenhuma falha detectada". O chamador decide que coleta vazia
// por falta de resposta é diferente de coleta vazia por ausência de falhas.
func parseRepadminShowRepl(raw string) []string {
	var falhas []string
	vistas := make(map[string]bool)

	for _, linha := range strings.Split(raw, "\n") {
		linha = strings.TrimSpace(strings.TrimRight(linha, "\r"))
		if linha == "" {
			continue
		}

		// Descarta explicitamente as linhas de sucesso antes de casar falha.
		if casaAlgum(linha, ignorarNaSaidaReplicacao) {
			continue
		}
		if !casaAlgum(linha, padroesDeFalhaReplicacao) {
			continue
		}

		// Ignora a linha que é só um resumo de contagem ("0 failures"), que o
		// primeiro padrão pode casar por conter um número.
		if ehResumoVazioDeFalhas(linha) {
			continue
		}

		mensagem := truncarAD(linha, maxADMessageLength)
		if vistas[mensagem] {
			continue
		}
		vistas[mensagem] = true
		falhas = append(falhas, mensagem)

		if len(falhas) >= maxADFailureMessages {
			break
		}
	}

	return falhas
}

// ehResumoVazioDeFalhas reconhece contadores que dizem "nenhuma falha"
// ("0 failures", "0 falhas", "Total failures: 0"). É PURA.
func ehResumoVazioDeFalhas(linha string) bool {
	lower := strings.ToLower(linha)
	if !strings.Contains(lower, "failure") && !strings.Contains(lower, "falha") &&
		!strings.Contains(lower, "error") && !strings.Contains(lower, "erro") {
		return false
	}
	// Procura um 0 isolado seguido da palavra de falha, ou a palavra seguida de 0.
	re := regexp.MustCompile(`(?i)(\b0\b\s*(failures?|falhas?|errors?|erros?)|\b(failures?|falhas?|errors?|erros?)\s*:\s*0\b)`)
	return re.MatchString(linha)
}

// ─────────────────────────────────────────────────────────────────────────────
// F5.2 — dcdiag /test:... : falhas nos testes de saúde do DC
// ─────────────────────────────────────────────────────────────────────────────

// regexDcdiagFalha reconhece as linhas de falha do dcdiag. O formato estável é:
//
//	Starting test: Replications
//	   ......................... DC01 failed test Replications
//
// e também "DC01 failed test <nome>" / "failed test <nome>". Casa os dois.
var regexDcdiagFalha = regexp.MustCompile(`(?i)failed\s+test\s+([A-Za-z0-9_-]+)`)

// regexDcdiagTesteIniciado captura o nome do teste em curso, usado como
// fallback quando a linha de falha não repete o nome.
var regexDcdiagTesteIniciado = regexp.MustCompile(`(?i)^\s*starting\s+test\s*:\s*([A-Za-z0-9_-]+)`)

// parseDcdiagOutput extrai os nomes dos testes que FALHARAM. É PURA.
//
// Devolve a lista de nomes de teste (deduplicada, ordenada na ordem de
// aparição) — vazia significa "nenhum teste falhou".
func parseDcdiagOutput(raw string) []string {
	var falhos []string
	vistos := make(map[string]bool)
	ultimoTeste := ""

	for _, linha := range strings.Split(raw, "\n") {
		linha = strings.TrimSpace(strings.TrimRight(linha, "\r"))
		if linha == "" {
			continue
		}

		if m := regexDcdiagTesteIniciado.FindStringSubmatch(linha); m != nil {
			ultimoTeste = m[1]
			continue
		}

		m := regexDcdiagFalha.FindStringSubmatch(linha)
		if m == nil {
			continue
		}

		nome := strings.TrimSpace(m[1])
		// Fallback: a linha de falha sem nome do teste usa o que estava em curso.
		if nome == "" {
			nome = ultimoTeste
		}
		if nome == "" || vistos[nome] {
			continue
		}
		vistos[nome] = true
		falhos = append(falhos, nome)

		if len(falhos) >= maxADFailedTests {
			break
		}
	}

	return falhos
}

// ─────────────────────────────────────────────────────────────────────────────
// F5.3 — w32tm /query /status : desvio de relógio
// ─────────────────────────────────────────────────────────────────────────────

// regexW32tmOffset captura o desvio de relógio do "w32tm /query /status".
//
// A linha é "Phase Offset: 0.0012345s" (ou "Clock Offset"). O sinal pode ser
// negativo. É PURA.
var regexW32tmOffset = regexp.MustCompile(`(?i)^\s*(phase|clock)\s+offset\s*:\s*([+-]?[0-9]+(?:[.,][0-9]+)?)\s*s?\s*$`)

// regexW32tmSource captura a fonte de tempo configurada ("Source: time.windows.com").
var regexW32tmSource = regexp.MustCompile(`(?i)^\s*source\s*:\s*(.+)$`)

// parseW32tmStatus extrai o desvio de relógio (em segundos) e a fonte de tempo.
// É PURA. ok=false quando o desvio não pôde ser lido.
func parseW32tmStatus(raw string) (offsetSeconds float64, source string, ok bool) {
	for _, linha := range strings.Split(raw, "\n") {
		linha = strings.TrimSpace(strings.TrimRight(linha, "\r"))
		if linha == "" {
			continue
		}

		if m := regexW32tmOffset.FindStringSubmatch(linha); m != nil {
			// Algumas localidades usam vírgula como separador decimal.
			valor := strings.Replace(m[2], ",", ".", 1)
			if parsed, err := strconv.ParseFloat(valor, 64); err == nil {
				offsetSeconds = parsed
				ok = true
			}
			continue
		}

		if m := regexW32tmSource.FindStringSubmatch(linha); m != nil && source == "" {
			origem := strings.TrimSpace(m[1])
			// "Source: Free-running System Clock" significa que NÃO há fonte
			// confiável — preservamos o texto para o técnico ver a causa raiz.
			source = truncarAD(origem, maxADMessageLength)
		}
	}

	return offsetSeconds, source, ok
}

// ─────────────────────────────────────────────────────────────────────────────
// Coleta
// ─────────────────────────────────────────────────────────────────────────────

// CollectADConsistency é o ponto de entrada EXPORTADO da telemetria de
// consistência de AD (F5).
//
// ⚠️ PRÉ-REQUISITO DE CONTRATO (não resolvido neste arquivo):
// o enum SensorType do SERVIDOR tem 19 valores (0..18, terminando em BitLocker)
// e NÃO possui um tipo para consistência de AD. Enquanto o servidor não expuser
// esse tipo, este coletor NÃO é agendável por policy — por isso ele é exportado
// como função (e não registrado num case de switch), evitando inventar no
// agente um contrato que o servidor não conhece e não consegue escalonar.
//
// Quando o tipo existir no servidor, o registro é UMA linha no switch de
// collectSensor, ao lado de SensorTypeCertificate.
// Devolve sensorCollection (tipo interno do pacote) porque é assim que todos os
// coletores se comunicam com o runner — exportar um alias só para esta função
// criaria uma segunda forma de dizer a mesma coisa.
func CollectADConsistency(ctx context.Context, cfg *SensorConfig) sensorCollection {
	return collectADConsistencySample(ctx, cfg)
}

// collectADConsistencySample coleta a evidência de consistência do AD.
//
// Roda SOMENTE em controlador de domínio. Em qualquer outra máquina devolve
// coleta vazia SEM erro — não é falha, é um sensor que não se aplica.
//
// Cada bloco é independente e best-effort: a falha de um não impede os outros.
// Se TODOS falharem, ainda devolvemos evidência com Errors preenchidos (o
// servidor decide o que fazer) em vez de um erro de coleta que esconderia o
// diagnóstico.
func collectADConsistencySample(ctx context.Context, cfg *SensorConfig) sensorCollection {
	if !isDomainControlerLocal() {
		// Não é DC: repadmin/dcdiag não têm o que consultar nesta máquina.
		return sensorCollection{}
	}

	evidence := adConsistencyEvidence{}

	// 7.1 — Replicação (repadmin /showrepl).
	if saida, err := runSensorPowerShell(ctx, minDuracao(adRepadminTimeout, cfg), "repadmin /showrepl"); err == nil {
		evidence.ReplicationChecked = true
		evidence.ReplicationFailures = 0
		mensagens := parseRepadminShowRepl(saida)
		evidence.ReplicationFailures = len(mensagens)
		evidence.ReplicationMessages = mensagens
	} else {
		evidence.Errors = append(evidence.Errors, "repadmin indisponivel: "+err.Error())
	}

	// 7.2 — Saúde do DC (dcdiag). Timeout curto: o dcdiag é lento e não pode
	// consumir o orçamento inteiro do sensor.
	if saida, err := runSensorPowerShell(ctx, minDuracao(adDcdiagTimeout, cfg), "dcdiag /test:DNS /test:Replications /test:Services /q"); err == nil {
		evidence.DcdiagChecked = true
		evidence.DcdiagFailedTests = parseDcdiagOutput(saida)
	} else {
		evidence.Errors = append(evidence.Errors, "dcdiag indisponivel: "+err.Error())
	}

	// 7.3 — Relógio (w32tm /query /status). Desvio > 15s quebra Kerberos, então
	// esta é a métrica mais acionável do bloco.
	if saida, err := runSensorPowerShell(ctx, minDuracao(adW32tmTimeout, cfg), "w32tm /query /status"); err == nil {
		if offset, source, ok := parseW32tmStatus(saida); ok {
			evidence.TimeChecked = true
			evidence.TimeOffsetSeconds = offset
			evidence.TimeSource = source
		} else {
			evidence.Errors = append(evidence.Errors, "w32tm sem linha de offset")
		}
	} else {
		evidence.Errors = append(evidence.Errors, "w32tm indisponivel: "+err.Error())
	}

	return buildADConsistencyCollection(evidence)
}

// buildADConsistencyCollection converte a evidência em canais + mensagem. É PURA
// (recebe a evidência já coletada), para poder ser testada sem executar nada.
func buildADConsistencyCollection(evidence adConsistencyEvidence) sensorCollection {
	channels := make(map[string]float64, 8)
	var partes []string

	// Coleta conseguiu falar com o AD? Sem isso o servidor não consegue
	// distinguir "AD saudável" de "não perguntamos".
	coletou := evidence.ReplicationChecked || evidence.DcdiagChecked || evidence.TimeChecked
	channels[ChannelADCollectOK] = boolToFloat(coletou)

	if evidence.ReplicationChecked {
		channels[ChannelADReplicationFailures] = float64(evidence.ReplicationFailures)
		channels[ChannelADReplicationOK] = boolToFloat(evidence.ReplicationFailures == 0)
		partes = append(partes, "replicacao: "+strconv.Itoa(evidence.ReplicationFailures)+" falha(s)")
		for _, mensagem := range evidence.ReplicationMessages {
			partes = append(partes, mensagem)
		}
	}

	if evidence.DcdiagChecked {
		channels[ChannelADDcdiagFailedTests] = float64(len(evidence.DcdiagFailedTests))
		channels[ChannelADDcdiagOK] = boolToFloat(len(evidence.DcdiagFailedTests) == 0)
		if len(evidence.DcdiagFailedTests) > 0 {
			partes = append(partes, "dcdiag falhou em: "+strings.Join(evidence.DcdiagFailedTests, ", "))
		} else {
			partes = append(partes, "dcdiag: nenhum teste falhou")
		}
	}

	if evidence.TimeChecked {
		channels[ChannelADTimeOffsetSeconds] = sensorRound2(evidence.TimeOffsetSeconds)
		// Tolerância de 15s: acima disso o Kerberos recusa tickets por "clock
		// skew too great". O limiar é evidência (o servidor decide alerta).
		channels[ChannelADClockOK] = boolToFloat(absFloat(evidence.TimeOffsetSeconds) <= 15)
		partes = append(partes, "relogio: desvio de "+formatarOffset(evidence.TimeOffsetSeconds)+"s")
		if evidence.TimeSource != "" {
			partes = append(partes, "fonte de tempo: "+evidence.TimeSource)
		}
	}

	// Erros compõem a mensagem mas NÃO viram Err de coleta: perder o repadmin
	// não pode apagar a evidência do dcdiag que veio boa.
	if len(evidence.Errors) > 0 {
		partes = append(partes, "falhas de coleta: "+strings.Join(evidence.Errors, "; "))
	}

	// Os MESMOS valores vão também em campos explícitos da Measure: o servidor
	// deriva os canais preferindo os campos e usa Channels como fallback. Assim
	// o contrato antigo (só canais) continua válido e o novo ganha tipagem.
	measure := SensorMeasure{
		AdCollectOK: sensorBool(coletou),
	}
	if evidence.ReplicationChecked {
		measure.AdReplicationFailures = sensorInt(evidence.ReplicationFailures)
		measure.AdReplicationOK = sensorBool(evidence.ReplicationFailures == 0)
	}
	if evidence.DcdiagChecked {
		measure.AdDcdiagFailedTests = sensorInt(len(evidence.DcdiagFailedTests))
		measure.AdDcdiagOK = sensorBool(len(evidence.DcdiagFailedTests) == 0)
	}
	if evidence.TimeChecked {
		measure.AdTimeOffsetSeconds = sensorFloat(sensorRound2(evidence.TimeOffsetSeconds))
		measure.AdClockOK = sensorBool(absFloat(evidence.TimeOffsetSeconds) <= 15)
	}

	collection := sensorCollection{Channels: channels, Measure: measure}
	if len(partes) > 0 {
		collection.Measure.ServiceState = sensorString(truncarAD(strings.Join(partes, " | "), 2000))
	}
	return collection
}

// ── Utilitários ──

// isDomainControlerLocal informa se ESTA máquina é controlador de domínio.
//
// Reutiliza sysctrl.IsDomainController — o predicado canônico de DC que já
// existe e já tem teste de coerência com DetectProductType
// (service_guard_windows_test.go:490). NÃO reimplementamos a leitura do
// registro nem duplicamos a lista de valores: uma segunda fonte de verdade
// para "é DC?" divergiria silenciosamente da primeira.
//
// Em cliente e servidor membro devolve false, e o coletor não gasta processo.
func isDomainControlerLocal() bool {
	return sysctrl.IsDomainController()
}

// boolToFloat converte bool em 1/0 para o mapa de canais numéricos.
func boolToFloat(v bool) float64 {
	if v {
		return 1
	}
	return 0
}

// absFloat devolve o valor absoluto.
func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// formatarOffset formata o desvio com sinal explícito e 3 casas.
func formatarOffset(v float64) string {
	return strconv.FormatFloat(v, 'f', 3, 64)
}

// truncarAD limita o tamanho de um texto enviado ao servidor.
func truncarAD(texto string, limite int) string {
	if len(texto) <= limite {
		return texto
	}
	if limite <= 3 {
		return texto[:limite]
	}
	return texto[:limite-3] + "..."
}

// casaAlgum informa se a linha casa com qualquer um dos padrões. É PURA.
func casaAlgum(linha string, padroes []*regexp.Regexp) bool {
	for _, padrao := range padroes {
		if padrao.MatchString(linha) {
			return true
		}
	}
	return false
}

// minDuracao escolhe o timeout do subprocesso: o menor entre o timeout do
// sensor e o teto do utilitário. Protege o orçamento do motor de sensores.
func minDuracao(teto time.Duration, cfg *SensorConfig) time.Duration {
	if cfg != nil && cfg.Timeout > 0 && cfg.Timeout < teto {
		return cfg.Timeout
	}
	return teto
}
