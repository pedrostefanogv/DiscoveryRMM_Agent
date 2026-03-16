// Package export provides inventory report export to Markdown and PDF formats.
package export

import (
	"sort"
	"strconv"
	"strings"

	"discovery/internal/models"
)

// BuildMarkdown renders the inventory report as a Markdown document.
// If redact is true, sensitive fields (serials, MACs, hostname) are masked.
func BuildMarkdown(r models.InventoryReport, redact bool) string {
	hw := r.Hardware
	if redact {
		hw = RedactHardware(hw)
	}

	var b strings.Builder
	b.WriteString("# Inventario Discovery\n\n")
	b.WriteString("- Coletado em: " + md(r.CollectedAt) + "\n")
	b.WriteString("- Fonte: " + md(r.Source) + "\n\n")

	b.WriteString("## Hardware\n\n")
	b.WriteString("- Hostname: " + md(hw.Hostname) + "\n")
	b.WriteString("- Fabricante: " + md(hw.Manufacturer) + "\n")
	b.WriteString("- Modelo: " + md(hw.Model) + "\n")
	b.WriteString("- CPU: " + md(hw.CPU) + "\n")
	b.WriteString("- Cores fisicos: " + strconv.Itoa(hw.Cores) + "\n")
	b.WriteString("- Cores logicos: " + strconv.Itoa(hw.LogicalCores) + "\n")
	b.WriteString("- Memoria (GB): " + strconv.FormatFloat(hw.MemoryGB, 'f', 2, 64) + "\n")
	b.WriteString("- Placa-mae fabricante: " + md(hw.MotherboardManufacturer) + "\n")
	b.WriteString("- Placa-mae modelo: " + md(hw.MotherboardModel) + "\n")
	b.WriteString("- Placa-mae serial: " + md(hw.MotherboardSerial) + "\n")
	b.WriteString("- BIOS vendor: " + md(hw.BIOSVendor) + "\n")
	b.WriteString("- BIOS versao: " + md(hw.BIOSVersion) + "\n")
	b.WriteString("- BIOS data: " + md(hw.BIOSReleaseDate) + "\n")
	b.WriteString("- BIOS serial: " + md(hw.BIOSSerial) + "\n")
	b.WriteString("- Quantidade de pentes: " + strconv.Itoa(hw.MemoryModulesCount) + "\n\n")

	b.WriteString("## Memoria (Pentes)\n\n")
	b.WriteString("| Slot | Banco | Fabricante | Part Number | Serial | Tamanho (GB) | Velocidade (MHz) | Tipo |\n")
	b.WriteString("| --- | --- | --- | --- | --- | ---: | ---: | --- |\n")
	for _, m := range r.MemoryModules {
		serial := m.Serial
		if redact {
			serial = Redacted
		}
		b.WriteString("| " + md(m.Slot) + " | " + md(m.Bank) + " | " + md(m.Manufacturer) + " | " + md(m.PartNumber) + " | " + md(serial) + " | " + strconv.FormatFloat(m.SizeGB, 'f', 2, 64) + " | " + strconv.Itoa(m.SpeedMHz) + " | " + md(m.Type) + " |\n")
	}
	b.WriteString("\n")

	b.WriteString("## Monitores\n\n")
	b.WriteString("| Nome | Fabricante | Serial | Resolucao | Status |\n")
	b.WriteString("| --- | --- | --- | --- | --- |\n")
	for _, m := range r.Monitors {
		serial := m.Serial
		if redact {
			serial = Redacted
		}
		b.WriteString("| " + md(m.Name) + " | " + md(m.Manufacturer) + " | " + md(serial) + " | " + md(m.Resolution) + " | " + md(m.Status) + " |\n")
	}
	b.WriteString("\n")

	b.WriteString("## GPUs\n\n")
	b.WriteString("| Nome | Fabricante | Driver | VRAM (GB) | Status |\n")
	b.WriteString("| --- | --- | --- | ---: | --- |\n")
	for _, g := range r.GPUs {
		b.WriteString("| " + md(g.Name) + " | " + md(g.Manufacturer) + " | " + md(g.DriverVersion) + " | " + strconv.FormatFloat(g.VRAMGB, 'f', 2, 64) + " | " + md(g.Status) + " |\n")
	}
	b.WriteString("\n")

	b.WriteString("## Sistema Operacional\n\n")
	b.WriteString("- Nome: " + md(r.OS.Name) + "\n")
	b.WriteString("- Versao: " + md(r.OS.Version) + "\n")
	b.WriteString("- Build: " + md(r.OS.Build) + "\n")
	b.WriteString("- Arquitetura: " + md(r.OS.Architecture) + "\n\n")

	b.WriteString("## Usuarios Logados\n\n")
	b.WriteString("| Usuario | Tipo | TTY | Host | PID | SID |\n")
	b.WriteString("| --- | --- | --- | --- | ---: | --- |\n")
	for _, u := range r.LoggedInUsers {
		sid := u.SID
		if redact {
			sid = Redacted
		}
		b.WriteString("| " + md(u.User) + " | " + md(u.Type) + " | " + md(u.TTY) + " | " + md(u.Host) + " | " + strconv.Itoa(u.PID) + " | " + md(sid) + " |\n")
	}
	b.WriteString("\n")

	b.WriteString("## Volumes\n\n")
	b.WriteString("| Dispositivo | Label | Tipo | FS | Tamanho (GB) | Livre (GB) | Serial |\n")
	b.WriteString("| --- | --- | --- | --- | ---: | ---: | --- |\n")
	for _, v := range r.Volumes {
		free := "-"
		if v.FreeKnown {
			free = strconv.FormatFloat(v.FreeGB, 'f', 2, 64)
		}
		serial := v.Serial
		if redact {
			serial = Redacted
		}
		b.WriteString("| " + md(v.Device) + " | " + md(v.Label) + " | " + md(v.Type) + " | " + md(v.FileSystem) + " | " + strconv.FormatFloat(v.SizeGB, 'f', 2, 64) + " | " + free + " | " + md(serial) + " |\n")
	}
	b.WriteString("\n")

	b.WriteString("## Redes\n\n")
	b.WriteString("| Interface | MAC | IPv4 | IPv6 | Gateway | Tipo | Status |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- |\n")
	for _, n := range r.Networks {
		mac := n.MAC
		if redact {
			mac = Redacted
		}
		b.WriteString("| " + md(n.Interface) + " | " + md(mac) + " | " + md(n.IPv4) + " | " + md(n.IPv6) + " | " + md(n.Gateway) + " | " + md(n.Type) + " | " + md(n.ConnectionStatus) + " |\n")
	}
	b.WriteString("\n")

	b.WriteString("## Startup Items\n\n")
	b.WriteString("| Nome | Path | Args | Tipo | Source | Status | Usuario |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- |\n")
	for _, s := range r.StartupItems {
		b.WriteString("| " + md(s.Name) + " | " + md(s.Path) + " | " + md(s.Args) + " | " + md(s.Type) + " | " + md(s.Source) + " | " + md(s.Status) + " | " + md(s.Username) + " |\n")
	}
	b.WriteString("\n")

	b.WriteString("## Autoexec\n\n")
	b.WriteString("| Nome | Path | Source |\n")
	b.WriteString("| --- | --- | --- |\n")
	for _, a := range r.Autoexec {
		b.WriteString("| " + md(a.Name) + " | " + md(a.Path) + " | " + md(a.Source) + " |\n")
	}
	b.WriteString("\n")

	b.WriteString("## Softwares\n\n")
	b.WriteString("| Nome | Versao | Publisher | ID Instalacao | Serial | Origem |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- |\n")

	software := append([]models.SoftwareItem(nil), r.Software...)
	sort.Slice(software, func(i, j int) bool {
		return strings.ToLower(software[i].Name) < strings.ToLower(software[j].Name)
	})

	for _, s := range software {
		serial := s.Serial
		if redact {
			serial = Redacted
		}
		b.WriteString("| " + md(s.Name) + " | " + md(s.Version) + " | " + md(s.Publisher) + " | " + md(s.InstallID) + " | " + md(serial) + " | " + md(s.Source) + " |\n")
	}

	return b.String()
}

// md sanitizes a string for use in a Markdown table cell.
func md(s string) string {
	v := strings.TrimSpace(s)
	if v == "" {
		return "-"
	}
	v = strings.ReplaceAll(v, "\n", " ")
	v = strings.ReplaceAll(v, "\r", " ")
	v = strings.ReplaceAll(v, "|", "\\|")
	return v
}
