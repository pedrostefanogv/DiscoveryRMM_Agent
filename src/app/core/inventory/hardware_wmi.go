package inventory

import (
	"context"
	"encoding/json"
	"log"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"discovery/app/core/models"
	"discovery/app/core/processutil"
)

// enrichHardwareFromWMI preenche campos vazios do HardwareInfo via WMI/CIM.
// Usado como fallback para VMs onde o DMI/SMBIOS não é exposto ao osquery
// (ex.: QEMU/KVM sem SMBIOS, Proxmox sem serial numbers).
func enrichHardwareFromWMI(hw *models.HardwareInfo) {
	if hw == nil || runtime.GOOS != "windows" {
		return
	}

	// Só executa WMI se houver campos vazios — evita custo em máquinas reais.
	needWMI := strings.TrimSpace(hw.SerialNumber) == "" ||
		strings.TrimSpace(hw.MotherboardManufacturer) == "" ||
		strings.TrimSpace(hw.MotherboardSerial) == "" ||
		strings.TrimSpace(hw.BIOSSerial) == "" ||
		strings.TrimSpace(hw.BIOSVendor) == ""

	if !needWMI {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Consulta única via PowerShell. Todos os campos de texto são forçados para
	// [string] via casting explícito para evitar que ConvertTo-Json serialize
	// $null como {} (objeto vazio) em vez de "" — comum em VMs Proxmox/QEMU
	// onde vários campos WMI/CIM não têm valor.
	// BIOSReleaseDate é formatado explicitamente porque o objeto DateTime do WMI
	// é serializado como sub-objeto pelo ConvertTo-Json.
	script := `$ErrorActionPreference = 'Stop'
$bios = Get-CimInstance Win32_BIOS -ErrorAction SilentlyContinue
$biosDate = ''
if ($bios -and $bios.ReleaseDate) { try { $biosDate = [string]($bios.ReleaseDate.ToString('yyyyMMddHHmmss')) } catch {} }
[PSCustomObject]@{
	SystemManufacturer  = [string](Get-CimInstance Win32_ComputerSystem -ErrorAction SilentlyContinue | Select-Object -ExpandProperty Manufacturer -ErrorAction SilentlyContinue)
	SystemModel         = [string](Get-CimInstance Win32_ComputerSystem -ErrorAction SilentlyContinue | Select-Object -ExpandProperty Model -ErrorAction SilentlyContinue)
	SystemSerial        = [string](Get-CimInstance Win32_BIOS -ErrorAction SilentlyContinue | Select-Object -ExpandProperty SerialNumber -ErrorAction SilentlyContinue)
	BaseboardMfr        = [string](Get-CimInstance Win32_BaseBoard -ErrorAction SilentlyContinue | Select-Object -ExpandProperty Manufacturer -ErrorAction SilentlyContinue)
	BaseboardProduct    = [string](Get-CimInstance Win32_BaseBoard -ErrorAction SilentlyContinue | Select-Object -ExpandProperty Product -ErrorAction SilentlyContinue)
	BaseboardSerial     = [string](Get-CimInstance Win32_BaseBoard -ErrorAction SilentlyContinue | Select-Object -ExpandProperty SerialNumber -ErrorAction SilentlyContinue)
	BIOSVendor          = [string](Get-CimInstance Win32_BIOS -ErrorAction SilentlyContinue | Select-Object -ExpandProperty Manufacturer -ErrorAction SilentlyContinue)
	BIOSVersion         = [string](Get-CimInstance Win32_BIOS -ErrorAction SilentlyContinue | Select-Object -ExpandProperty SMBIOSBIOSVersion -ErrorAction SilentlyContinue)
	BIOSReleaseDate     = $biosDate
	BIOSSerial          = [string](Get-CimInstance Win32_BIOS -ErrorAction SilentlyContinue | Select-Object -ExpandProperty SerialNumber -ErrorAction SilentlyContinue)
} | ConvertTo-Json -Depth 1 -Compress`

	cmd := exec.CommandContext(ctx, "powershell",
		"-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden",
		"-Command", script)
	processutil.HideWindow(cmd)

	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("[inventory] fallback WMI falhou: %v", err)
		return
	}

	var wmi struct {
		SystemManufacturer string `json:"SystemManufacturer"`
		SystemModel        string `json:"SystemModel"`
		SystemSerial       string `json:"SystemSerial"`
		BaseboardMfr       string `json:"BaseboardMfr"`
		BaseboardProduct   string `json:"BaseboardProduct"`
		BaseboardSerial    string `json:"BaseboardSerial"`
		BIOSVendor         string `json:"BIOSVendor"`
		BIOSVersion        string `json:"BIOSVersion"`
		BIOSReleaseDate    string `json:"BIOSReleaseDate"`
		BIOSSerial         string `json:"BIOSSerial"`
	}
	if err := json.Unmarshal(output, &wmi); err != nil {
		// Fallback: WMI pode retornar campos como {} (objeto vazio) em VMs
		// em vez de "" quando o valor é $null. Tenta parse via RawMessage
		// para extrair apenas os campos string.
		wmi = parseFlexibleWMIFields(output)
		if wmi == (struct {
			SystemManufacturer string `json:"SystemManufacturer"`
			SystemModel        string `json:"SystemModel"`
			SystemSerial       string `json:"SystemSerial"`
			BaseboardMfr       string `json:"BaseboardMfr"`
			BaseboardProduct   string `json:"BaseboardProduct"`
			BaseboardSerial    string `json:"BaseboardSerial"`
			BIOSVendor         string `json:"BIOSVendor"`
			BIOSVersion        string `json:"BIOSVersion"`
			BIOSReleaseDate    string `json:"BIOSReleaseDate"`
			BIOSSerial         string `json:"BIOSSerial"`
		}{}) {
			log.Printf("[inventory] fallback WMI parse falhou: %v (output=%s)", err, strings.TrimSpace(string(output)))
			return
		}
		log.Printf("[inventory] fallback WMI: parse flexivel bem-sucedido apos erro=%v", err)
	}

	log.Printf("[inventory] fallback WMI: preenchendo campos vazios do hardware")

	// Só preenche se o campo estiver vazio (osquery tem precedência sobre WMI).
	if strings.TrimSpace(hw.SerialNumber) == "" {
		hw.SerialNumber = sanitizeWMIValue(wmi.SystemSerial)
	}
	if strings.TrimSpace(hw.MotherboardManufacturer) == "" {
		hw.MotherboardManufacturer = sanitizeWMIValue(wmi.BaseboardMfr)
	}
	if strings.TrimSpace(hw.MotherboardModel) == "" {
		hw.MotherboardModel = sanitizeWMIValue(wmi.BaseboardProduct)
	}
	if strings.TrimSpace(hw.MotherboardSerial) == "" {
		hw.MotherboardSerial = sanitizeWMIValue(wmi.BaseboardSerial)
	}
	if strings.TrimSpace(hw.BIOSVendor) == "" {
		hw.BIOSVendor = sanitizeWMIValue(wmi.BIOSVendor)
	}
	if strings.TrimSpace(hw.BIOSVersion) == "" {
		hw.BIOSVersion = sanitizeWMIValue(wmi.BIOSVersion)
	}
	if strings.TrimSpace(hw.BIOSReleaseDate) == "" {
		hw.BIOSReleaseDate = wmiBIOSReleaseDate(wmi.BIOSReleaseDate)
	}
	if strings.TrimSpace(hw.BIOSSerial) == "" {
		hw.BIOSSerial = sanitizeWMIValue(wmi.BIOSSerial)
	}
}

// sanitizeWMIValue limpa valores WMI e ignora sentinelas "Default string" comuns.
func sanitizeWMIValue(v string) string {
	v = strings.TrimSpace(v)
	if strings.EqualFold(v, "Default string") || strings.EqualFold(v, "To be filled by O.E.M.") || strings.EqualFold(v, "System Serial Number") {
		return ""
	}
	return v
}

// wmiBIOSReleaseDate converte data WMI (formato Microsoft: YYYYMMDDHHMMSS.ffffff+OFF)
// para RFC3339 simplificado (YYYY-MM-DD).
func wmiBIOSReleaseDate(wmiDate string) string {
	wmiDate = strings.TrimSpace(wmiDate)
	if wmiDate == "" {
		return ""
	}
	// WMI date format: 20260502210000.000000+000
	// Extrai só a parte da data (8 primeiros dígitos).
	if len(wmiDate) >= 8 {
		y := wmiDate[0:4]
		m := wmiDate[4:6]
		d := wmiDate[6:8]
		return y + "-" + m + "-" + d
	}
	return ""
}

// parseFlexibleWMIFields é um fallback que usa RawMessage para tolerar campos
// WMI que venham como objetos vazios ({}) em vez de strings quando $null.
// Comum em VMs Proxmox/QEMU onde vários campos CIM não têm valor.
func parseFlexibleWMIFields(output []byte) struct {
	SystemManufacturer string `json:"SystemManufacturer"`
	SystemModel        string `json:"SystemModel"`
	SystemSerial       string `json:"SystemSerial"`
	BaseboardMfr       string `json:"BaseboardMfr"`
	BaseboardProduct   string `json:"BaseboardProduct"`
	BaseboardSerial    string `json:"BaseboardSerial"`
	BIOSVendor         string `json:"BIOSVendor"`
	BIOSVersion        string `json:"BIOSVersion"`
	BIOSReleaseDate    string `json:"BIOSReleaseDate"`
	BIOSSerial         string `json:"BIOSSerial"`
} {
	type flexibleStrings struct {
		SystemManufacturer json.RawMessage `json:"SystemManufacturer"`
		SystemModel        json.RawMessage `json:"SystemModel"`
		SystemSerial       json.RawMessage `json:"SystemSerial"`
		BaseboardMfr       json.RawMessage `json:"BaseboardMfr"`
		BaseboardProduct   json.RawMessage `json:"BaseboardProduct"`
		BaseboardSerial    json.RawMessage `json:"BaseboardSerial"`
		BIOSVendor         json.RawMessage `json:"BIOSVendor"`
		BIOSVersion        json.RawMessage `json:"BIOSVersion"`
		BIOSReleaseDate    json.RawMessage `json:"BIOSReleaseDate"`
		BIOSSerial         json.RawMessage `json:"BIOSSerial"`
	}

	var fs flexibleStrings
	if err := json.Unmarshal(output, &fs); err != nil {
		return struct {
			SystemManufacturer string `json:"SystemManufacturer"`
			SystemModel        string `json:"SystemModel"`
			SystemSerial       string `json:"SystemSerial"`
			BaseboardMfr       string `json:"BaseboardMfr"`
			BaseboardProduct   string `json:"BaseboardProduct"`
			BaseboardSerial    string `json:"BaseboardSerial"`
			BIOSVendor         string `json:"BIOSVendor"`
			BIOSVersion        string `json:"BIOSVersion"`
			BIOSReleaseDate    string `json:"BIOSReleaseDate"`
			BIOSSerial         string `json:"BIOSSerial"`
		}{}
	}

	rawToStr := func(raw json.RawMessage) string {
		if len(raw) == 0 {
			return ""
		}
		// Se começa com aspas, é uma string JSON — tenta extrair
		if raw[0] == '"' {
			var s string
			if err := json.Unmarshal(raw, &s); err == nil {
				return s
			}
		}
		// Se é um objeto ({...}) ou outro tipo não-string, retorna vazio
		if raw[0] == '{' || raw[0] == '[' {
			return ""
		}
		// Última tentativa: trata como string pura
		return strings.TrimSpace(string(raw))
	}

	return struct {
		SystemManufacturer string `json:"SystemManufacturer"`
		SystemModel        string `json:"SystemModel"`
		SystemSerial       string `json:"SystemSerial"`
		BaseboardMfr       string `json:"BaseboardMfr"`
		BaseboardProduct   string `json:"BaseboardProduct"`
		BaseboardSerial    string `json:"BaseboardSerial"`
		BIOSVendor         string `json:"BIOSVendor"`
		BIOSVersion        string `json:"BIOSVersion"`
		BIOSReleaseDate    string `json:"BIOSReleaseDate"`
		BIOSSerial         string `json:"BIOSSerial"`
	}{
		SystemManufacturer: rawToStr(fs.SystemManufacturer),
		SystemModel:        rawToStr(fs.SystemModel),
		SystemSerial:       rawToStr(fs.SystemSerial),
		BaseboardMfr:       rawToStr(fs.BaseboardMfr),
		BaseboardProduct:   rawToStr(fs.BaseboardProduct),
		BaseboardSerial:    rawToStr(fs.BaseboardSerial),
		BIOSVendor:         rawToStr(fs.BIOSVendor),
		BIOSVersion:        rawToStr(fs.BIOSVersion),
		BIOSReleaseDate:    rawToStr(fs.BIOSReleaseDate),
		BIOSSerial:         rawToStr(fs.BIOSSerial),
	}
}
