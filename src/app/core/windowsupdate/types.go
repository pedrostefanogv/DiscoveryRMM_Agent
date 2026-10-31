// Package windowsupdate implementa o inventário e a instalação de
// atualizações do Windows (KB) no agente, usando WUA (Microsoft.Update.*) via
// COM e Win32_QuickFixEngineering para as instaladas.
//
// O pacote é dividido em três partes:
//   - tipos e normalização de chaves (este arquivo e keys.go), portáveis;
//   - implementação Windows (client_windows.go, qfe_windows.go, reboot_windows.go,
//     policy_windows.go) construída sobre go-ole e o registro;
//   - stubs para plataformas sem WUA (client_other.go etc.).
package windowsupdate

import (
	"errors"
	"time"
)

// Erros tipados do pacote. O chamador (comando remoto/inventário) traduz esses
// erros em códigos estáveis para o servidor — nunca em texto livre.
var (
	// ErrUnsupportedOS indica plataforma sem WUA (build não-Windows).
	ErrUnsupportedOS = errors.New("windows update nao suportado nesta plataforma")
	// ErrWuaUnavailable indica que o COM/WUA nao pode ser inicializado
	// (Windows Update Agent ausente/corrompido).
	ErrWuaUnavailable = errors.New("windows update agent indisponivel")
	// ErrServiceDisabled indica que o servico Windows Update esta parado ou
	// desabilitado. O agente nao tenta reabilitar.
	ErrServiceDisabled = errors.New("servico windows update desabilitado")
	// ErrTimeout indica que a operacao excedeu o limite. O COM nao e
	// cancelavel: a chamada original pode continuar em segundo plano.
	ErrTimeout = errors.New("tempo esgotado na operacao do windows update")
	// ErrBusy indica que outra operacao WUA (scan/install/hide) esta em
	// andamento. As operacoes sao serializadas de proposito.
	ErrBusy = errors.New("outra operacao de windows update ja esta em andamento")
	// ErrSuperseded indica que o update deixou de existir no catalogo entre a
	// aprovacao e a instalacao.
	ErrSuperseded = errors.New("atualizacao substituida ou removida do catalogo")
	// ErrPolicyManagedByGPO indica que a maquina e gerenciada por politica
	// corporativa (WSUS/Intune): o agente NAO sobrescreve essa politica.
	ErrPolicyManagedByGPO = errors.New("politica de windows update gerenciada por gpo/wsus")
)

// Origens possiveis de um update.
const (
	SourceWUA = "wua"
	SourceQFE = "qfe"
)

// Status de scan reportados ao servidor.
const (
	ScanStatusOK          = "ok"
	ScanStatusError       = "error"
	ScanStatusTimeout     = "timeout"
	ScanStatusUnsupported = "unsupported"
)

// Codigos de erro de scan (estaveis para a API).
const (
	ScanErrorNone            = ""
	ScanErrorUnsupported     = "unsupported"
	ScanErrorWuaUnavailable  = "wua_unavailable"
	ScanErrorServiceDisabled = "service_disabled"
	ScanErrorTimeout         = "timeout"
	ScanErrorFailed          = "scan_failed"
)

// Update representa uma atualizacao do Windows normalizada.
type Update struct {
	// UpdateKey e a chave canonica: "guid:<GUID>:<revisao>" (pendentes; o GUID
	// e o IUpdateIdentity.UpdateID) ou "kb:KB5034441" (instaladas via QFE).
	UpdateKey string `json:"updateKey"`
	// UpdateID e o GUID do IUpdateIdentity ("" para linhas de QFE).
	UpdateID string `json:"updateId,omitempty"`
	// Revision e o IUpdateIdentity.RevisionNumber (0 para QFE).
	Revision int `json:"revision,omitempty"`
	// KBIDs lista os KBArticleIDs do update (pode ter mais de um).
	KBIDs []string `json:"kbIds,omitempty"`
	// KBID e o primeiro KB normalizado (atalho para exibição/política).
	KBID string `json:"kbId,omitempty"`

	Title          string   `json:"title"`
	Description    string   `json:"description,omitempty"`
	Classification string   `json:"classification,omitempty"`
	Categories     []string `json:"categories,omitempty"`
	// CategoryIDs sao os IUpdateCategory.CategoryID (GUID estavel, independente
	// de idioma). Nao vao para o servidor: servem para classificar localmente
	// (WU-14).
	CategoryIDs []string `json:"-"`
	Severity       string   `json:"severity,omitempty"`
	// SeveritySource diz se a severidade veio do fornecedor (MSRC) ou foi
	// derivada pelo agente (decisao D10). Vazio em linhas de QFE (instaladas),
	// que nao tem rating.
	SeveritySource string `json:"severitySource,omitempty"`
	// UpdateType: feature | driver | software (WU-15). Calculado no agente para a
	// API nao re-derivar por titulo.
	UpdateType string `json:"updateType,omitempty"`
	// AvailableVersion: versão disponível de um app de terceiros (WU-19/WU-20).
	// Usada na assinatura de mudança e exibida na UI; vazia para WUA/QFE.
	AvailableVersion string `json:"availableVersion,omitempty"`

	IsPending      bool `json:"isPending"`
	IsInstalled    bool `json:"isInstalled"`
	IsDownloaded   bool `json:"isDownloaded"`
	IsMandatory    bool `json:"isMandatory"`
	IsHidden       bool `json:"isHidden"`
	RequiresReboot bool `json:"requiresReboot"`

	// MaxDownloadSize e IUpdate.MaxDownloadSize (bytes) como o WUA reporta.
	// ATENÇÃO: o WUA reporta o tamanho MÁXIMO do pacote completo (todas as
	// variações/idiomas), que pode ser ~10x o download real que a tela do
	// Windows mostra (ex.: Insider Preview: WUA=97.7 GB vs Windows=9,1 GB).
	// A UI exibe "a partir de min"/"≈ max" quando plausível e omite acima do
	// limiar — nao tratar divergencia como bug de unidade.
	MaxDownloadSize int64  `json:"maxDownloadSize,omitempty"`
	// MinDownloadSize (IUpdate.MinDownloadSize, bytes): menor download possível
	// (express/ Delta). A UI usa "a partir de X" quando > 0.
	MinDownloadSize int64  `json:"minDownloadSize,omitempty"`
	SupportURL      string `json:"supportUrl,omitempty"`

	PublishedAt    *time.Time `json:"publishedAt,omitempty"`
	LastDeployedAt *time.Time `json:"lastDeployedAt,omitempty"`
	// InstalledAt so existe para linhas instaladas (QFE.InstalledOn).
	InstalledAt *time.Time `json:"installedAt,omitempty"`

	Source string `json:"source"`
}

// ScanOptions parametriza um scan.
type ScanOptions struct {
	// IncludeInstalled inclui as KBs instaladas (QFE). Default true.
	IncludeInstalled bool
	// IncludeHidden inclui updates ocultos (para reconciliar hide/unhide).
	IncludeHidden bool
	// IncludeDrivers inclui drivers (default false; fora do escopo).
	IncludeDrivers bool
}

// Motivos de descarte de um update no scan (estaveis para log/API). Nunca
// descartamos em silencio: todo update fora do inventario aparece aqui.
const (
	SkipReasonDriver     = "driver"
	SkipReasonNoIdentity = "no_identity"
	SkipReasonUnreadable = "unreadable"
)

// ScanResult e o resultado de um scan.
type ScanResult struct {
	Updates        []Update `json:"updates"`
	PendingCount   int      `json:"pendingCount"`
	InstalledCount int      `json:"installedCount"`
	SkippedCount   int      `json:"skippedCount"`
	// SkippedReasons detalha o SkippedCount por motivo (driver, no_identity...).
	SkippedReasons map[string]int `json:"skippedReasons,omitempty"`
	// UnreadableErr guarda o erro COM do ultimo item ilegivel (diagnostico de
	// pendentes que ficam fora do inventario por falha de leitura). Consumido
	// apenas no log do agente; nunca enviado ao servidor.
	UnreadableErr string `json:"-"`
	// Partial indica que uma das fontes falhou (ex.: QFE) e o resultado é
	// incompleto — o servidor não deve tratar listas vazias como remoção.
	Partial       bool       `json:"partial,omitempty"`
	PendingReboot RebootInfo `json:"pendingReboot"`
	// State e a sonda barata do estado local (reboot, ultima instalacao,
	// download em andamento, servico). Alimenta a assinatura que dispara o scan.
	State      WindowsUpdateState `json:"state,omitempty"`
	StartedAt  time.Time          `json:"startedAt"`
	DurationMs int64              `json:"durationMs"`
	Source     string             `json:"source"`
}

// AddSkipped contabiliza um update descartado com o motivo estavel.
func (r *ScanResult) AddSkipped(reason string) {
	if r == nil {
		return
	}
	r.SkippedCount++
	if r.SkippedReasons == nil {
		r.SkippedReasons = make(map[string]int, 4)
	}
	r.SkippedReasons[reason]++
}

// RebootInfo e o resultado da sonda de reinício pendente.
type RebootInfo struct {
	Pending   bool      `json:"pending"`
	Reasons   []string  `json:"reasons,omitempty"`
	CheckedAt time.Time `json:"checkedAt"`
}

// WindowsUpdateState e a sonda BARATA do estado local do Windows Update.
// Nao toca no WUA (nenhum objeto COM, nenhuma busca): le registro, servico e
// o log de instalacao. Serve para (a) decidir quando um scan completo e
// necessario (quando a assinatura muda) e (b) explicar a UI mesmo quando o
// scan WUA nao pode rodar (servico desabilitado, WUA ausente).
type WindowsUpdateState struct {
	// Supported=false em plataformas/maquinas sem WUA.
	Supported bool `json:"supported"`
	// ServiceState: running, stopped, disabled, manual, automatic, unknown.
	ServiceState string `json:"serviceState,omitempty"`
	// WuOnline: o log de instalacao mostra atividade recente (download/install).
	WuOnline bool `json:"wuOnline"`
	// PolicyManaged / WSUSServer: politica corporativa detectada.
	PolicyManaged bool   `json:"policyManaged"`
	WSUSServer    string `json:"wsusServer,omitempty"`

	RebootPending bool     `json:"rebootPending"`
	RebootReasons []string `json:"rebootReasons,omitempty"`

	// LastInstallSuccess: ultima atualizacao instalada com sucesso (UTC).
	LastInstallSuccess *time.Time `json:"lastInstallSuccess,omitempty"`
	// LastInstallTime: ultima linha de instalacao do WindowsUpdate.log (UTC),
	// inclusive falhas — muda durante o download/instalacao.
	LastInstallTime *time.Time `json:"lastInstallTime,omitempty"`
	// DownloadInProgress: o log mostra download em andamento sem instalacao
	// concluida depois dele.
	DownloadInProgress bool `json:"downloadInProgress"`
	// DownloadProgressPercent: ultima porcentagem de download vista no log
	// (0-100); 0 = desconhecido (omitido no JSON). NAO participa da assinatura
	// do estado: cada 1% mudaria o hash e dispararia um scan completo.
	DownloadProgressPercent int `json:"downloadProgressPercent,omitempty"`

	// Agregado de KBs instaladas lido do registro (barato: sem WMI).
	// QFEInstalledCountKnown=false quando a leitura do CBS falhou: o servidor
	// deve PRESERVAR a contagem conhecida em vez de gravar 0.
	QFEInstalledCount      int  `json:"qfeInstalledCount"`
	QFEInstalledCountKnown bool `json:"qfeInstalledCountKnown"`
	RecentQFEs        []string `json:"recentQfes,omitempty"`

	AuOptions        int    `json:"auOptions,omitempty"`
	NoAutoUpdate     bool   `json:"noAutoUpdate"`
	DeferralDays     int    `json:"deferralDays,omitempty"`
	ActiveHoursStart *int   `json:"activeHoursStart,omitempty"`
	ActiveHoursEnd   *int   `json:"activeHoursEnd,omitempty"`
	// PolicyKnown=true quando a politica de WU foi LIDA (inclusive "nenhuma
	// politica configurada"): permite ao servidor LIMPAR valores antigos quando
	// o WSUS/deferral sai da maquina. false = leitura falhou, preserva o
	// valor conhecido.
	PolicyKnown     bool   `json:"policyKnown"`
	PolicyErrorCode string `json:"policyErrorCode,omitempty"`

	CheckedAt time.Time `json:"checkedAt"`
}

// Motivos de reinício pendente (estaveis para a API).
const (
	RebootReasonWindowsUpdate  = "windows_update"
	RebootReasonCBS            = "cbs"
	RebootReasonFileRename     = "file_rename"
	RebootReasonComputerRename = "computer_rename"
	RebootReasonServicing      = "servicing"
)

// InstallTarget identifica um update a instalar. O agente NUNCA instala por
// titulo: a chave e revalidada contra o catalogo atual do WUA.
type InstallTarget struct {
	UpdateKey string   `json:"updateKey"`
	UpdateID  string   `json:"updateId,omitempty"`
	Revision  int      `json:"revision,omitempty"`
	KBIDs     []string `json:"kbIds,omitempty"`
}

// Motivos de SKIP de instalacao (estaveis para a API). Skip NAO e falha:
// "already_installed" = o update ja esta na maquina; "not_offered" = o update
// saiu do catalogo (superseded/expirado) entre a aprovacao e a execucao.
const (
	InstallSkipAlreadyInstalled = "already_installed"
	InstallSkipNotOffered       = "not_offered"
)

// InstallOutcome e o resultado por update.
type InstallOutcome struct {
	UpdateKey      string `json:"updateKey"`
	KBID           string `json:"kbId,omitempty"`
	ResultCode     int    `json:"resultCode,omitempty"`
	RebootRequired bool   `json:"rebootRequired"`
	Error          string `json:"error,omitempty"`
	// SkipReason preenchido quando o update nao foi instalado mas tambem nao
	// falhou (already_installed | not_offered). Vazio = resultado normal.
	SkipReason string `json:"skipReason,omitempty"`
}

// InstallResult e o resultado de uma instalacao.
type InstallResult struct {
	Installed []InstallOutcome `json:"installed"`
	Failed    []InstallOutcome `json:"failed"`
	// Skipped sao alvos que nao foram instalados e NAO sao falha (WU-07):
	// ja instalados ou que sairam do catalogo. Ficam visiveis no resultado.
	Skipped []InstallOutcome `json:"skipped,omitempty"`
	// NotFound e mantido para compatibilidade (agentes/servidores antigos):
	// hoje todo alvo ausente vira Skipped ou Failed, nunca NotFound silencioso.
	NotFound       []string `json:"notFound,omitempty"`
	RebootRequired bool     `json:"rebootRequired"`
	// RestorePointCreated/Error documentam o checkpoint de sistema (WU-17).
	RestorePointCreated bool   `json:"restorePointCreated,omitempty"`
	RestorePointError   string `json:"restorePointError,omitempty"`
	DurationMs          int64  `json:"durationMs"`
}

// Code = 0 quando todos instalaram, 1 quando houve falha parcial/total.
func (r *InstallResult) Code() int {
	if r == nil {
		return 1
	}
	if len(r.Failed) > 0 || len(r.NotFound) > 0 {
		return 1
	}
	// Sucesso = algo instalado OU tudo legitimamente pulado (ja instalado /
	// fora do catalogo). Skip nunca derruba o lote.
	if len(r.Installed) == 0 && len(r.Skipped) == 0 {
		return 1
	}
	return 0
}

// ProbeInfo descreve o estado do WUA/servico/politica na maquina.
type ProbeInfo struct {
	Supported     bool   `json:"supported"`
	ServiceState  string `json:"serviceState,omitempty"`
	WSUSServer    string `json:"wsusServer,omitempty"`
	PolicyManaged bool   `json:"policyManaged"`
	AgentVersion  string `json:"agentVersion,omitempty"`
	ErrorCode     string `json:"errorCode,omitempty"`
}
