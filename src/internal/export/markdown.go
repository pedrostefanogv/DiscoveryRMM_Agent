// Package export provides inventory report export to Markdown and PDF formats.
package export

import (
	"fmt"
	"sort"
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

	writeKV := func(key, value string) {
		fmt.Fprintf(&b, "- %s: %s\n", key, md(value))
	}

	b.WriteString("# Inventario Discovery\n\n")
	fmt.Fprintf(&b, "- Coletado em: %s\n", md(r.CollectedAt))
	fmt.Fprintf(&b, "- Fonte: %s\n\n", md(r.Source))

	b.WriteString("## Hardware\n\n")
	writeKV("Hostname", hw.Hostname)
	writeKV("Fabricante", hw.Manufacturer)
	writeKV("Modelo", hw.Model)
	writeKV("Serial Number", hw.SerialNumber)
	writeKV("CPU", hw.CPU)
	fmt.Fprintf(&b, "- Cores fisicos: %d\n", hw.Cores)
	fmt.Fprintf(&b, "- Cores logicos: %d\n", hw.LogicalCores)
	fmt.Fprintf(&b, "- Memoria (GB): %.2f\n", hw.MemoryGB)
	writeKV("Placa-mae fabricante", hw.MotherboardManufacturer)
	writeKV("Placa-mae modelo", hw.MotherboardModel)
	writeKV("Placa-mae serial", hw.MotherboardSerial)
	writeKV("BIOS vendor", hw.BIOSVendor)
	writeKV("BIOS versao", hw.BIOSVersion)
	writeKV("BIOS data", hw.BIOSReleaseDate)
	writeKV("BIOS serial", hw.BIOSSerial)
	fmt.Fprintf(&b, "- Quantidade de pentes: %d\n\n", hw.MemoryModulesCount)

	b.WriteString("## Memoria (Pentes)\n\n")
	b.WriteString("| Slot | Banco | Fabricante | Part Number | Serial | Tamanho (GB) | Velocidade (MHz) | Tipo |\n")
	b.WriteString("| --- | --- | --- | --- | --- | ---: | ---: | --- |\n")
	for _, m := range r.MemoryModules {
		serial := m.Serial
		if redact {
			serial = Redacted
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %.2f | %d | %s |\n",
			md(m.Slot), md(m.Bank), md(m.Manufacturer), md(m.PartNumber),
			md(serial), m.SizeGB, m.SpeedMHz, md(m.Type))
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
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n",
			md(m.Name), md(m.Manufacturer), md(serial), md(m.Resolution), md(m.Status))
	}
	b.WriteString("\n")

	b.WriteString("## GPUs\n\n")
	b.WriteString("| Nome | Fabricante | Driver | VRAM (GB) | Status |\n")
	b.WriteString("| --- | --- | --- | ---: | --- |\n")
	for _, g := range r.GPUs {
		fmt.Fprintf(&b, "| %s | %s | %s | %.2f | %s |\n",
			md(g.Name), md(g.Manufacturer), md(g.DriverVersion), g.VRAMGB, md(g.Status))
	}
	b.WriteString("\n")

	b.WriteString("## Sistema Operacional\n\n")
	writeKV("Nome", r.OS.Name)
	writeKV("Versao", r.OS.Version)
	writeKV("Build", r.OS.Build)
	writeKV("Arquitetura", r.OS.Architecture)
	b.WriteString("\n")

	b.WriteString("## Usuarios Logados\n\n")
	b.WriteString("| Usuario | Tipo | TTY | Host | PID | SID |\n")
	b.WriteString("| --- | --- | --- | --- | ---: | --- |\n")
	for _, u := range r.LoggedInUsers {
		sid := u.SID
		if redact {
			sid = Redacted
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %d | %s |\n",
			md(u.User), md(u.Type), md(u.TTY), md(u.Host), u.PID, md(sid))
	}
	b.WriteString("\n")

	b.WriteString("## Volumes\n\n")
	b.WriteString("| Dispositivo | Label | Tipo | FS | Tamanho (GB) | Livre (GB) | Serial |\n")
	b.WriteString("| --- | --- | --- | --- | ---: | ---: | --- |\n")
	for _, v := range r.Volumes {
		serial := v.Serial
		if redact {
			serial = Redacted
		}
		var free string
		if v.FreeKnown {
			free = fmt.Sprintf("%.2f", v.FreeGB)
		} else {
			free = "-"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %.2f | %s | %s |\n",
			md(v.Device), md(v.Label), md(v.Type), md(v.FileSystem), v.SizeGB, free, md(serial))
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
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s |\n",
			md(n.Interface), md(mac), md(n.IPv4), md(n.IPv6), md(n.Gateway),
			md(n.Type), md(n.ConnectionStatus))
	}
	b.WriteString("\n")

	b.WriteString("## Startup Items\n\n")
	b.WriteString("| Nome | Path | Args | Tipo | Source | Status | Usuario |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- |\n")
	for _, s := range r.StartupItems {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s |\n",
			md(s.Name), md(s.Path), md(s.Args), md(s.Type), md(s.Source),
			md(s.Status), md(s.Username))
	}
	b.WriteString("\n")

	b.WriteString("## Autoexec\n\n")
	b.WriteString("| Nome | Path | Source |\n")
	b.WriteString("| --- | --- | --- |\n")
	for _, a := range r.Autoexec {
		fmt.Fprintf(&b, "| %s | %s | %s |\n", md(a.Name), md(a.Path), md(a.Source))
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
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s |\n",
			md(s.Name), md(s.Version), md(s.Publisher), md(s.InstallID), md(serial), md(s.Source))
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
