//go:build windows

package native

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"discovery/app/core/processutil"
)

// ─────────────────────────────────────────────────────────────────────────────
// F10 — Hardware de servidor: controladora RAID por CLI de fornecedor + BMC.
//
// Só roda em Windows Server (ver winserver_windows.go). Em cliente Windows o
// coletor devolve struct vazia com motivo, sem executar nenhum comando.
//
// DECISÃO DE ESCOPO — BMC in-band (iDRAC/iLO) NÃO é consultado aqui:
// uma leitura in-band de BMC exige credencial (racadm/hponcfg/IPMI autenticam
// no controlador). Guardar essa credencial no agente é uma decisão de segurança
// que o plano adiou explicitamente. Portanto este arquivo apenas DETECTA a
// presença das ferramentas e registra o fato — nunca chama com credencial.
//
// Todas as CLIs de RAID são SOMENTE-LEITURA (comando de status). Nenhum
// comando de escrita (criar/apagar array, blink LED, clear foreign config) é
// executado — mesma postura read-only do restante do inventário.
// ─────────────────────────────────────────────────────────────────────────────

// raidToolTimeout limita cada CLI de fornecedor. CLIs de RAID falam com a
// controladora pelo barramento e podem travar quando o firmware está ocupado
// (reconstrução em andamento); um timeout curto evita prender o inventário.
const raidToolTimeout = 12 * time.Second

// maxRaidCommandOutput limita o volume lido da saída das CLIs. storcli com
// muitos discos produz saída longa; sem teto, um array grande infla o payload.
const maxRaidCommandOutput = 2 << 20 // 2 MiB

// raidTool descreve uma CLI de controladora RAID suportada.
type raidTool struct {
	// Name é o executável procurado no PATH e nos diretórios conhecidos.
	Name string
	// Vendor identifica a família da controladora para o operador.
	Vendor string
	// Args é o comando SOMENTE-LEITURA de status.
	Args []string
}

// raidTools são as CLIs detectadas, na ordem de preferência. A ordem importa
// quando duas famílias convivem: a primeira encontrada é a consultada.
//
// Broadcom/LSI: storcli (mais novo) > perccli (Dell) > MegaCli (legado) — os
// três falam com a MESMA controladora; storcli tem JSON, MegaCli só texto.
// HPE (ssacli) e Adaptec (arcconf) são famílias distintas.
var raidTools = []raidTool{
	{Name: "storcli", Vendor: "Broadcom/LSI", Args: []string{"/cALL", "show", "all", "J"}},
	{Name: "perccli", Vendor: "Dell PERC", Args: []string{"/cALL", "show", "all", "J"}},
	{Name: "MegaCli", Vendor: "Broadcom/LSI (legado)", Args: []string{"-AdpAllInfo", "-aALL"}},
	{Name: "ssacli", Vendor: "HPE Smart Array", Args: []string{"ctrl", "all", "show", "config"}},
	{Name: "arcconf", Vendor: "Adaptec", Args: []string{"GETCONFIG", "1"}},
	{Name: "omreport", Vendor: "Dell OpenManage", Args: []string{"storage", "controller"}},
}

// raidToolDirs são diretórios conhecidos onde as CLIs de fornecedor se
// instalam e que NÃO estão no PATH do serviço. Os caminhos relativos são
// resolvidos sob Program Files / Program Files (x86).
var raidToolDirs = []string{
	`C:\storcli`,
	`C:\perccli`,
	`C:\MegaCli`,
	`Dell\SysMgt\oma\bin`,
	`Dell\SysMgt\iDRAC Tools`,
	`Smart Storage Administrator\ssacli\bin`,
	`Compaq\Hpacucli\Bin`,
	`Adaptec\maxView Storage Manager`,
	`Broadcom\storcli`,
}

// bmcTool descreve uma ferramenta de BMC detectável. NÃO é executada — ver
// decisão de escopo no cabeçalho.
type bmcTool struct {
	Name    string
	Vendor  string
	SubDirs []string
}

// bmcTools são as ferramentas de BMC procuradas. Apenas presença.
var bmcTools = []bmcTool{
	{Name: "iDRAC Tools", Vendor: "Dell", SubDirs: []string{`Dell\SysMgt\iDRAC Tools`, `Dell\SysMgt\oma`}},
	{Name: "HP Lights-Out", Vendor: "HPE", SubDirs: []string{`Hewlett-Packard\hponcfg`, `Compaq\hponcfg`}},
	{Name: "IPMI", Vendor: "genérico", SubDirs: []string{`Intel\IPMI`, `Supermicro\IPMICFG`}},
}

// ServerHardware é o resultado do F10. Campos vazios significam "não
// aplicável" (cliente Windows) ou "nada detectado" — Reason explica qual.
type ServerHardware struct {
	// IsServer replica o ProductType para o consumidor não precisar reler o
	// registro.
	IsServer bool `json:"isServer"`
	// RAIDTools é a lista de CLIs de RAID efetivamente ENCONTRADAS no disco.
	RAIDTools []RAIDToolReport `json:"raidTools,omitempty"`
	// BMC lista as ferramentas de BMC detectadas (presença, sem consulta).
	BMC []BMCDetection `json:"bmc,omitempty"`
	// Reason explica o resultado vazio quando nada foi detectado.
	Reason string `json:"reason,omitempty"`
}

// RAIDToolReport é o resultado da consulta de UMA CLI de RAID.
type RAIDToolReport struct {
	Name   string `json:"name"`
	Vendor string `json:"vendor"`
	// Path é o caminho absoluto do executável encontrado.
	Path string `json:"path,omitempty"`
	// OK indica que o comando de status rodou e retornou saída.
	OK bool `json:"ok"`
	// Error descreve a falha (nunca derruba o coletor).
	Error string `json:"error,omitempty"`
	// Controllers é a contagem de controladoras reconhecidas na saída.
	Controllers int `json:"controllers,omitempty"`
	// VirtualDisks e PhysicalDisks são contagens derivadas da saída JSON
	// (storcli/perccli). Para CLIs somente-texto ficam zeradas.
	VirtualDisks  int `json:"virtualDisks,omitempty"`
	PhysicalDisks int `json:"physicalDisks,omitempty"`
	// Degraded lista os discos/arrays que a própria controladora reportou
	// como degradados ou falhos — o sinal acionável do monitoramento.
	Degraded []string `json:"degraded,omitempty"`
}

// BMCDetection registra a presença de uma ferramenta de BMC. Sem credencial,
// sem chamada — apenas o fato de a ferramenta existir na máquina.
type BMCDetection struct {
	Name   string `json:"name"`
	Vendor string `json:"vendor"`
	Path   string `json:"path,omitempty"`
}

// CollectServerHardware detecta CLIs de RAID/BMC e, se houver alguma CLI de
// RAID, consulta o status (somente-leitura).
//
// Contrato: nunca retorna erro e nunca entra em panic. Em cliente Windows
// devolve vazio com motivo. Cada CLI é independente — a falha de uma não
// impede a consulta das demais.
func CollectServerHardware(ctx context.Context) ServerHardware {
	if runtime.GOOS != "windows" {
		return ServerHardware{Reason: "nao-windows"}
	}

	result := ServerHardware{IsServer: IsWindowsServer()}

	// BMC primeiro: a detecção é barata (só olha o disco) e vale mesmo em
	// servidor sem controladora RAID reconhecida.
	result.BMC = detectBMCTools()

	for _, tool := range raidTools {
		path := findRaidTool(tool.Name)
		if path == "" {
			continue
		}
		report := RAIDToolReport{Name: tool.Name, Vendor: tool.Vendor, Path: path}
		fillRAIDReport(ctx, &report, path, tool.Args)
		result.RAIDTools = append(result.RAIDTools, report)
	}

	if len(result.RAIDTools) == 0 && len(result.BMC) == 0 {
		// Motivo distinto entre "não é servidor" e "é servidor mas não tem
		// ferramenta": o operador precisa saber qual dos dois é o caso.
		if result.IsServer {
			result.Reason = "nenhuma-ferramenta-raid-ou-bmc-detectada"
		} else {
			result.Reason = "nao-e-windows-server"
		}
	}
	return result
}

// findRaidTool procura o executável no PATH e nos diretórios conhecidos.
// Devolve o caminho encontrado ou "" — nunca erro.
func findRaidTool(name string) string {
	if path, err := exec.LookPath(name); err == nil && path != "" {
		return path
	}
	// Variantes com extensão: algumas CLIs só existem como .exe/.bat.
	for _, candidate := range []string{name + ".exe", name + ".bat"} {
		if path, err := exec.LookPath(candidate); err == nil && path != "" {
			return path
		}
	}
	for _, dir := range raidToolDirs {
		if !filepath.IsAbs(dir) {
			// Caminho relativo a Program Files / Program Files (x86).
			for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)"} {
				root := os.Getenv(env)
				if root == "" {
					continue
				}
				if found := probeToolFile(filepath.Join(root, dir), name); found != "" {
					return found
				}
			}
			continue
		}
		if found := probeToolFile(dir, name); found != "" {
			return found
		}
	}
	return ""
}

// probeToolFile testa as extensões usuais dentro de um diretório candidato.
func probeToolFile(dir, name string) string {
	for _, candidate := range []string{name, name + ".exe", name + ".bat", name + ".cmd"} {
		full := filepath.Join(dir, candidate)
		if info, err := os.Stat(full); err == nil && !info.IsDir() {
			return full
		}
	}
	return ""
}

// detectBMCTools procura as ferramentas de BMC e devolve o que existir.
// Nenhuma é executada — ver decisão de escopo no topo do arquivo.
func detectBMCTools() []BMCDetection {
	var found []BMCDetection
	for _, tool := range bmcTools {
		for _, sub := range tool.SubDirs {
			for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)"} {
				root := os.Getenv(env)
				if root == "" {
					continue
				}
				dir := filepath.Join(root, sub)
				if info, err := os.Stat(dir); err == nil && info.IsDir() {
					found = append(found, BMCDetection{
						Name:   tool.Name,
						Vendor: tool.Vendor,
						Path:   dir,
					})
					break // uma ferramenta conta uma vez
				}
			}
		}
	}
	return found
}

// fillRAIDReport executa o comando de status da CLI e preenche o relatório.
// Falha é registrada em Error e não propaga.
func fillRAIDReport(ctx context.Context, report *RAIDToolReport, path string, args []string) {
	toolCtx, cancel := context.WithTimeout(ctx, raidToolTimeout)
	defer cancel()

	cmd := exec.CommandContext(toolCtx, path, args...)
	processutil.HideWindow(cmd)

	output, err := cmd.CombinedOutput()
	if len(output) > maxRaidCommandOutput {
		output = output[:maxRaidCommandOutput]
	}
	if err != nil {
		// Timeout e "controladora ausente" chegam aqui — ambos são esperados
		// em máquina sem RAID, então não é erro fatal.
		if toolCtx.Err() == context.DeadlineExceeded {
			report.Error = "timeout"
		} else {
			report.Error = "falha-ao-executar"
		}
		return
	}

	text := strings.TrimSpace(string(output))
	if text == "" {
		report.Error = "saida-vazia"
		return
	}
	report.OK = true

	// storcli/perccli emitem JSON com "J". Tentamos interpretar; se não for
	// JSON (CLIs de texto), as contagens ficam zeradas e só OK é reportado.
	parseRAIDJSON(report, text)
}

// parseRAIDJSON extrai contagens e discos degradados da saída JSON do
// storcli/perccli. É tolerante: qualquer formato inesperado resulta em
// contagens zeradas, nunca em erro.
func parseRAIDJSON(report *RAIDToolReport, text string) {
	var envelope map[string]any
	if err := json.Unmarshal([]byte(text), &envelope); err != nil {
		return // CLI de texto — esperado para MegaCli/ssacli/arcconf/omreport
	}
	controllers, ok := envelope["Controllers"].([]any)
	if !ok {
		return
	}
	report.Controllers = len(controllers)

	for _, c := range controllers {
		ctrl, ok := c.(map[string]any)
		if !ok {
			continue
		}
		// storcli agrupa a resposta em "Response Data" -> "Controller N".
		responses, ok := ctrl["Response Data"].(map[string]any)
		if !ok {
			continue
		}
		for key, value := range responses {
			lower := strings.ToLower(key)
			block, ok := value.(map[string]any)
			if !ok {
				continue
			}
			// As chaves reais do storcli/perccli variam por versão e por
			// controladora: "Virtual Drives", "VD0", "PDs for VD0",
			// "Drive /c0/e252/s3", "Physical Drives". Casamos por família de
			// substring e checamos o VD ANTES do PD, porque "PDs for VD0"
			// contém as duas palavras.
			switch {
			case strings.Contains(lower, "virtual drive") || strings.HasPrefix(lower, "vd"):
				report.VirtualDisks += len(block)
				report.Degraded = append(report.Degraded, scanDegraded(block, "vd")...)
			case strings.Contains(lower, "pd") || strings.Contains(lower, "physical drive") || strings.Contains(lower, "drive /c"):
				report.PhysicalDisks += len(block)
				report.Degraded = append(report.Degraded, scanDegraded(block, "pd")...)
			}
		}
	}
}

// scanDegraded procura estados de falha nas entradas de um bloco JSON.
// Só reporta o que a PRÓPRIA controladora marcou — não inferimos saúde.
func scanDegraded(block map[string]any, prefix string) []string {
	var degraded []string
	for id, raw := range block {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		for field, value := range entry {
			lowerField := strings.ToLower(field)
			if !strings.Contains(lowerField, "state") && !strings.Contains(lowerField, "status") {
				continue
			}
			state := strings.ToLower(wmiString(map[string]any{"v": value}, "v"))
			if isDegradedRAIDState(state) {
				degraded = append(degraded, prefix+":"+id+"="+state)
				break
			}
		}
	}
	return degraded
}

// degradedRAIDStates são os estados de falha reportados pelas controladoras
// Broadcom/LSI e HPE. A lista é de SUBSTRING: "Degraded", "Rbld", "Failed".
var degradedRAIDStates = []string{"degraded", "failed", "offline", "critical", "rebuild", "rbld"}

// isDegradedRAIDState informa se um estado textual indica falha/degradacao.
// É PURA para poder ser testada sem hardware.
func isDegradedRAIDState(state string) bool {
	lower := strings.ToLower(strings.TrimSpace(state))
	if lower == "" {
		return false
	}
	// "optimal"/"online"/"ok" são os estados saudáveis — nunca degradados.
	for _, ok := range []string{"optimal", "online", "ok", "good", "normal"} {
		if lower == ok {
			return false
		}
	}
	for _, bad := range degradedRAIDStates {
		if strings.Contains(lower, bad) {
			return true
		}
	}
	return false
}

// serverHardwareAvailable indica se o host tem qualquer fonte de hardware de
// servidor. Usado pelo chamador para decidir se inclui a seção no payload.
func serverHardwareAvailable(h ServerHardware) bool {
	return len(h.RAIDTools) > 0 || len(h.BMC) > 0
}
