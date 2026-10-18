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
	Severity       string   `json:"severity,omitempty"`

	IsPending      bool `json:"isPending"`
	IsInstalled    bool `json:"isInstalled"`
	IsDownloaded   bool `json:"isDownloaded"`
	IsMandatory    bool `json:"isMandatory"`
	IsHidden       bool `json:"isHidden"`
	RequiresReboot bool `json:"requiresReboot"`

	MaxDownloadSize int64  `json:"maxDownloadSize,omitempty"`
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

// ScanResult e o resultado de um scan.
type ScanResult struct {
	Updates        []Update `json:"updates"`
	PendingCount   int      `json:"pendingCount"`
	InstalledCount int      `json:"installedCount"`
	SkippedCount   int      `json:"skippedCount"`
	// Partial indica que uma das fontes falhou (ex.: QFE) e o resultado é
	// incompleto — o servidor não deve tratar listas vazias como remoção.
	Partial       bool       `json:"partial,omitempty"`
	PendingReboot RebootInfo `json:"pendingReboot"`
	StartedAt     time.Time  `json:"startedAt"`
	DurationMs    int64      `json:"durationMs"`
	Source        string     `json:"source"`
}

// RebootInfo e o resultado da sonda de reinício pendente.
type RebootInfo struct {
	Pending   bool      `json:"pending"`
	Reasons   []string  `json:"reasons,omitempty"`
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

// InstallOutcome e o resultado por update.
type InstallOutcome struct {
	UpdateKey      string `json:"updateKey"`
	KBID           string `json:"kbId,omitempty"`
	ResultCode     int    `json:"resultCode,omitempty"`
	RebootRequired bool   `json:"rebootRequired"`
	Error          string `json:"error,omitempty"`
}

// InstallResult e o resultado de uma instalacao.
type InstallResult struct {
	Installed      []InstallOutcome `json:"installed"`
	Failed         []InstallOutcome `json:"failed"`
	NotFound       []string         `json:"notFound,omitempty"`
	RebootRequired bool             `json:"rebootRequired"`
	DurationMs     int64            `json:"durationMs"`
}

// Code = 0 quando todos instalaram, 1 quando houve falha parcial/total.
func (r *InstallResult) Code() int {
	if r == nil {
		return 1
	}
	if len(r.Failed) > 0 || len(r.NotFound) > 0 {
		return 1
	}
	if len(r.Installed) == 0 {
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
