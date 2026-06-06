package inventory

import (
	"context"
	"encoding/json"
	"log"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"discovery/internal/models"
	"discovery/internal/processutil"
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

	// Consulta única via PowerShell — mais rápido que 3 chamadas CIM separadas.
	script := `$ErrorActionPreference = 'Stop'
[PSCustomObject]@{
	SystemManufacturer  = (Get-CimInstance Win32_ComputerSystem -ErrorAction SilentlyContinue | Select-Object -ExpandProperty Manufacturer -ErrorAction SilentlyContinue)
	SystemModel         = (Get-CimInstance Win32_ComputerSystem -ErrorAction SilentlyContinue | Select-Object -ExpandProperty Model -ErrorAction SilentlyContinue)
	SystemSerial        = (Get-CimInstance Win32_BIOS -ErrorAction SilentlyContinue | Select-Object -ExpandProperty SerialNumber -ErrorAction SilentlyContinue)
	BaseboardMfr        = (Get-CimInstance Win32_BaseBoard -ErrorAction SilentlyContinue | Select-Object -ExpandProperty Manufacturer -ErrorAction SilentlyContinue)
	BaseboardProduct    = (Get-CimInstance Win32_BaseBoard -ErrorAction SilentlyContinue | Select-Object -ExpandProperty Product -ErrorAction SilentlyContinue)
	BaseboardSerial     = (Get-CimInstance Win32_BaseBoard -ErrorAction SilentlyContinue | Select-Object -ExpandProperty SerialNumber -ErrorAction SilentlyContinue)
	BIOSVendor          = (Get-CimInstance Win32_BIOS -ErrorAction SilentlyContinue | Select-Object -ExpandProperty Manufacturer -ErrorAction SilentlyContinue)
	BIOSVersion         = (Get-CimInstance Win32_BIOS -ErrorAction SilentlyContinue | Select-Object -ExpandProperty SMBIOSBIOSVersion -ErrorAction SilentlyContinue)
	BIOSReleaseDate     = (Get-CimInstance Win32_BIOS -ErrorAction SilentlyContinue | Select-Object -ExpandProperty ReleaseDate -ErrorAction SilentlyContinue)
	BIOSSerial          = (Get-CimInstance Win32_BIOS -ErrorAction SilentlyContinue | Select-Object -ExpandProperty SerialNumber -ErrorAction SilentlyContinue)
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
		log.Printf("[inventory] fallback WMI parse falhou: %v (output=%s)", err, strings.TrimSpace(string(output)))
		return
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
