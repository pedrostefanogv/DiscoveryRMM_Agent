//go:build windows

package native

import (
	"context"
	"encoding/json"
	"os/exec"
	"sort"
	"strings"
	"time"

	"discovery/app/core/processutil"
)

// ─────────────────────────────────────────────────────────────────────────────
// F3 — Inventário de roles/features do Windows Server (Get-WindowsFeature).
//
// MOTIVO: o inventário do agente descrevia o HARDWARE do servidor, mas não o que
// ele FAZ. Sem saber quais roles (AD DS, DNS, DHCP, IIS, Hyper-V, RDS, File
// Server, Failover Clustering…) estão instaladas, o servidor RMM não consegue
// segmentar frota, nem correlacionar alertas a um papel real da máquina.
//
// CUSTO ZERO EM CLIENTE: esta coleta roda SOMENTE quando ProductType indica
// Windows Server (isServerProductType, ver winserver_windows.go). Em estação de
// trabalho devolve lista vazia sem sequer iniciar um processo — o cmdlet
// Get-WindowsFeature existe apenas no Windows Server.
//
// FONTE PRIMÁRIA: cmdlet Get-WindowsFeature (módulo ServerManager). Funciona
// tanto em Server com Desktop Experience quanto em Server Core — é o caminho
// suportado e o único que traz InstallState legível por humano.
// FALLBACK: DISM /online /Get-Features, que responde quando o módulo
// ServerManager está quebrado/ausente (imagens reduzidas, Server Core com
// remoção do módulo). O DISM não traz DisplayName em todos os idiomas, então
// usamos o Name como rótulo.
//
// Best-effort em TODOS os caminhos: falha de coleta devolve lista vazia + o
// motivo no campo Error, e nunca panic. Um servidor sem roles reportadas não
// pode derrubar a coleta inteira do inventário.
// ─────────────────────────────────────────────────────────────────────────────

// WindowsRole é uma role/feature do Windows Server reportada ao servidor RMM.
//
// Os nomes de campo JSON são o contrato com o servidor: name, displayName,
// installed, installState. Renomear qualquer um quebra o outro lado.
type WindowsRole struct {
	// Name é o nome técnico canônico (ex.: "AD-Domain-Services", "IIS-WebServer").
	// É a chave estável para diff/inventário — o DisplayName é localizado.
	Name string `json:"name"`
	// DisplayName é o rótulo de exibição (ex.: "Serviços de Domínio do Active
	// Directory"). Pode variar com o idioma do SO; por isso nunca é chave.
	DisplayName string `json:"displayName,omitempty"`
	// Installed indica se a role está instalada nesta máquina.
	Installed bool `json:"installed"`
	// InstallState é o estado bruto do Windows ("Installed", "Available",
	// "Removed", "InstallPending"…), preservado para não perdermos nuance que
	// o booleano Installed não expressa.
	InstallState string `json:"installState,omitempty"`
}

// WindowsRolesResult é o resultado da coleta de roles: a lista ordenada e um
// motivo de erro quando a coleta não pôde ser feita.
//
// Error é string (e não error) porque este resultado vira payload JSON: o
// servidor precisa distinguir "servidor sem roles instaladas" de "não
// conseguimos perguntar". Lista vazia + Error vazio = cliente Windows (não se
// aplica); lista vazia + Error preenchido = falha de coleta.
type WindowsRolesResult struct {
	Roles []WindowsRole `json:"roles"`
	// Source registra de onde veio o dado: "Get-WindowsFeature" ou "DISM".
	Source string `json:"source,omitempty"`
	// Error descreve por que a coleta falhou (vazio = sucesso).
	Error string `json:"error,omitempty"`
}

// Limites de sanidade da coleta. O Windows Server tem ~250 features; 500 dá
// folga larga sem permitir que uma saída corrompida/infinita vire payload.
const (
	maxWindowsRoles      = 500
	windowsRolesTimeout  = 45 * time.Second
	maxDisplayNameLength = 200
	maxRoleNameLength    = 100
)

// powershellGetWindowsFeatureScript consulta o módulo ServerManager e emite
// JSON compacto. Os nomes de propriedade que este script publica (Name,
// DisplayName, Installed, InstallState) são o contrato lido por
// parseWindowsFeatureJSON — mudar um lado exige mudar o outro.
//
// Import-Module é explícito porque a sessão pode ter sido iniciada sem o
// módulo autocarregado (Server Core); se ele não existir, o script falha e o
// chamador cai no fallback DISM.
const powershellGetWindowsFeatureScript = `$ErrorActionPreference = 'Stop'
Import-Module ServerManager -ErrorAction Stop
Get-WindowsFeature | ForEach-Object {
  [PSCustomObject]@{
    Name         = $_.Name
    DisplayName  = $_.DisplayName
    Installed    = [bool]$_.Installed
    InstallState = [string]$_.InstallState
  }
} | ConvertTo-Json -Compress -Depth 3`

// parseWindowsFeatureJSON converte a saída JSON do Get-WindowsFeature na lista
// de roles. É PURA (não executa nada) para ser testável sem PowerShell.
//
// Trata as três formas que o ConvertTo-Json pode produzir: array, objeto único
// (quando há exatamente 1 feature) e saída vazia/nula. Roles sem Name são
// descartadas (o Name é a chave); o resultado sai ordenado por Name para que o
// diff entre coletas seja estável.
func parseWindowsFeatureJSON(raw string) []WindowsRole {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "[]" || trimmed == "null" {
		return nil
	}

	var rows []map[string]any
	if err := json.Unmarshal([]byte(trimmed), &rows); err != nil {
		// Objeto único: o PowerShell não emite array quando há 1 item.
		var single map[string]any
		if err2 := json.Unmarshal([]byte(trimmed), &single); err2 != nil {
			return nil
		}
		rows = []map[string]any{single}
	}

	roles := make([]WindowsRole, 0, len(rows))
	for _, row := range rows {
		name := strings.TrimSpace(jsonStringField(row, "Name"))
		if name == "" {
			continue
		}
		if len(name) > maxRoleNameLength {
			name = name[:maxRoleNameLength]
		}

		display := strings.TrimSpace(jsonStringField(row, "DisplayName"))
		if len(display) > maxDisplayNameLength {
			display = display[:maxDisplayNameLength]
		}
		// Sem DisplayName o Name é o melhor rótulo disponível: melhor exibir
		// "AD-Domain-Services" do que uma linha sem identificação na UI.
		if display == "" {
			display = name
		}

		roles = append(roles, WindowsRole{
			Name:         name,
			DisplayName:  display,
			Installed:    jsonBoolField(row, "Installed"),
			InstallState: strings.TrimSpace(jsonStringField(row, "InstallState")),
		})

		if len(roles) >= maxWindowsRoles {
			break
		}
	}

	sortWindowsRolesByName(roles)
	return roles
}

// parseDismFeaturesOutput converte a saída TEXTO do
// "dism /online /Get-Features" na lista de roles. É PURA.
//
// O formato é estável e por blocos:
//
//	Feature Name : IIS-WebServer
//	State : Enabled
//
// Só o State "Enabled" conta como instalado. O DISM não publica DisplayName,
// então o Name é replicado no campo de exibição.
func parseDismFeaturesOutput(raw string) []WindowsRole {
	var roles []WindowsRole
	var pendingName string

	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, ""))
		if line == "" {
			continue
		}

		key, value, found := splitDismLine(line)
		if !found {
			continue
		}

		switch strings.ToLower(key) {
		case "feature name":
			pendingName = value
		case "state":
			if pendingName == "" {
				continue
			}
			name := pendingName
			pendingName = ""

			if len(name) > maxRoleNameLength {
				name = name[:maxRoleNameLength]
			}
			roles = append(roles, WindowsRole{
				Name:         name,
				DisplayName:  name,
				Installed:    strings.EqualFold(value, "Enabled"),
				InstallState: value,
			})
			if len(roles) >= maxWindowsRoles {
				sortWindowsRolesByName(roles)
				return roles
			}
		}
	}

	sortWindowsRolesByName(roles)
	return roles
}

// splitDismLine separa "Chave : Valor" tolerando espaços irregulares em volta
// dos dois-pontos (o DISM alinha as colunas com padding variável). É PURA.
func splitDismLine(line string) (key, value string, ok bool) {
	idx := strings.Index(line, ":")
	if idx < 0 {
		return "", "", false
	}
	// Ignora linhas de cabeçalho/rodapé que não têm o formato chave: valor.
	key = strings.TrimSpace(line[:idx])
	value = strings.TrimSpace(line[idx+1:])
	if key == "" || value == "" {
		return "", "", false
	}
	return key, value, true
}

// sortWindowsRolesByName ordena por Name (case-insensitive) com desempate
// estável. Ordem determinística é requisito do diff do inventário: sem ela, a
// mesma máquina geraria payloads diferentes a cada coleta.
func sortWindowsRolesByName(roles []WindowsRole) {
	sort.SliceStable(roles, func(i, j int) bool {
		li, lj := strings.ToLower(roles[i].Name), strings.ToLower(roles[j].Name)
		if li == lj {
			return roles[i].Name < roles[j].Name
		}
		return li < lj
	})
}

// jsonStringField extrai um campo string de um mapa JSON.
func jsonStringField(row map[string]any, key string) string {
	v, ok := row[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// jsonBoolField extrai um campo booleano de um mapa JSON.
//
// Aceita bool nativo (o caminho normal) e também as formas que
// ConvertTo-Json/JSON variantes produzem: string "True"/"False"/"true" e
// número 1/0. Sem essa tolerância um $true serializado como string viraria
// false silenciosamente — e a role apareceria como não instalada.
func jsonBoolField(row map[string]any, key string) bool {
	v, ok := row[key]
	if !ok || v == nil {
		return false
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return strings.EqualFold(strings.TrimSpace(t), "true")
	case float64:
		return t != 0
	default:
		return false
	}
}

// CollectWindowsRoles é o ponto de entrada EXPORTADO do inventário de roles,
// no mesmo formato dos outros coletores de servidor (CollectHyperVInventario,
// CollectClusterInventario, CollectStorageRDSInventario).
//
// Nunca devolve erro e nunca panic: o resultado carrega a lista e, quando a
// coleta falha, o motivo em Error. Quem consome (inventário) decide se publica.
func CollectWindowsRoles(ctx context.Context) WindowsRolesResult {
	return collectWindowsRolesNative(ctx)
}

// collectWindowsRolesNative detecta e coleta as roles/features instaladas.
//
// Roda SOMENTE em Windows Server: em cliente devolve resultado vazio e sem
// erro, sem sequer criar processo (o cmdlet não existe lá e o custo seria
// puro desperdício a cada ciclo de inventário).
//
// Falha de qualquer estratégia é NÃO-fatal: o resultado carrega o motivo em
// Error e a lista fica vazia. Nunca propaga erro e nunca panic.
func collectWindowsRolesNative(ctx context.Context) WindowsRolesResult {
	if !isServerProductType(detectProductType()) {
		// Cliente Windows: roles de servidor não se aplicam. Sem erro — não é
		// uma falha, é uma pergunta que não faz sentido nesta máquina.
		return WindowsRolesResult{}
	}

	if roles, err := collectWindowsRolesViaPowerShell(ctx); err == nil && len(roles) > 0 {
		return WindowsRolesResult{Roles: roles, Source: "Get-WindowsFeature"}
	}

	if roles, err := collectWindowsRolesViaDism(ctx); err == nil && len(roles) > 0 {
		return WindowsRolesResult{Roles: roles, Source: "DISM"}
	}

	return WindowsRolesResult{
		Error: "nao foi possivel enumerar roles: Get-WindowsFeature e DISM indisponiveis",
	}
}

// collectWindowsRolesViaPowerShell executa o cmdlet Get-WindowsFeature.
func collectWindowsRolesViaPowerShell(ctx context.Context) ([]WindowsRole, error) {
	runCtx, cancel := context.WithTimeout(ctx, windowsRolesTimeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "powershell",
		"-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden",
		"-Command", powershellGetWindowsFeatureScript)
	processutil.HideWindow(cmd)

	output, err := cmd.CombinedOutput()
	if err != nil {
		// Módulo ServerManager ausente, PowerShell bloqueado por política/AV,
		// ou timeout: o chamador decide cair no fallback DISM.
		return nil, err
	}

	return parseWindowsFeatureJSON(string(output)), nil
}

// collectWindowsRolesViaDism executa "dism /online /Get-Features" como
// fallback. O DISM não precisa de PowerShell e responde em imagens onde o
// módulo ServerManager foi removido.
func collectWindowsRolesViaDism(ctx context.Context) ([]WindowsRole, error) {
	runCtx, cancel := context.WithTimeout(ctx, windowsRolesTimeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "dism", "/online", "/Get-Features")
	processutil.HideWindow(cmd)

	output, err := cmd.CombinedOutput()
	if err != nil {
		// O DISM devolve exit code != 0 em algumas imagens mesmo produzindo
		// saída útil; por isso ainda tentamos parsear o que veio.
		if roles := parseDismFeaturesOutput(string(output)); len(roles) > 0 {
			return roles, nil
		}
		return nil, err
	}

	return parseDismFeaturesOutput(string(output)), nil
}
