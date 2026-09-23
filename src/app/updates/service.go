package updates

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"discovery/app/core/chocolatey"
	"discovery/app/core/models"
)

// AppsService defines the package manager surface used by updates.
type AppsService interface {
	ListUpgradable(ctx context.Context) (string, error)
	ListUpgradableChocolatey(ctx context.Context) (string, error)
	ListInstalled(ctx context.Context) (string, error)
	ListInstalledChocolatey(ctx context.Context) (string, error)
}

// ActivityFunc starts and ends a user-visible activity.
type ActivityFunc func(string) func()

// Options wires the updates service.
type Options struct {
	Apps          AppsService
	BeginActivity ActivityFunc
	Logf          func(string)
	Now           func() time.Time
	Ctx           func() context.Context
}

// Service handles update discovery and package actions.
type Service struct {
	apps          AppsService
	beginActivity ActivityFunc
	logf          func(string)
	now           func() time.Time
	ctx           func() context.Context
}

const (
	packageActionInstall   = "install"
	packageActionUninstall = "uninstall"
	packageActionUpgrade   = "upgrade"
)

// NewService builds a updates service.
func NewService(opts Options) *Service {
	logf := opts.Logf
	if logf == nil {
		logf = func(string) {}
	}
	beginActivity := opts.BeginActivity
	if beginActivity == nil {
		beginActivity = func(string) func() { return nil }
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Service{
		apps:          opts.Apps,
		beginActivity: beginActivity,
		logf:          logf,
		now:           now,
		ctx:           opts.Ctx,
	}
}

// GetPendingUpdates runs `winget upgrade` and parses the output into structured items.
func (s *Service) GetPendingUpdates() ([]models.UpgradeItem, error) {
	done := s.beginActivity("checagem de atualizacoes")
	if done != nil {
		defer done()
	}
	ctx := context.Background()
	if s.ctx != nil {
		ctx = s.ctx()
	}
	raw, err := s.apps.ListUpgradable(ctx)
	s.logf("[winget upgrade] " + s.now().Format("15:04:05"))
	s.logf(raw)
	if err != nil {
		return nil, err
	}
	items := parseUpgradeOutput(raw)

	rawChocolatey, chocolateyErr := s.apps.ListUpgradableChocolatey(ctx)
	s.logf("[choco outdated] " + s.now().Format("15:04:05"))
	if strings.TrimSpace(rawChocolatey) != "" {
		s.logf(rawChocolatey)
	}
	if chocolateyErr != nil {
		s.logf("[choco outdated] erro: " + chocolateyErr.Error())
	} else {
		items = append(items, parseChocolateyOutdatedOutput(rawChocolatey)...)
	}

	items = dedupeUpgradeItems(items)
	return items, nil
}

// ParseUpgradeOutput exposes the parsing logic for tests and callers.
func ParseUpgradeOutput(raw string) []models.UpgradeItem {
	return parseUpgradeOutput(raw)
}

// ParseInstalledOutput exposes the installed list parsing.
func ParseInstalledOutput(raw string) []string {
	return parseInstalledOutput(raw)
}

// ParseInstalledListOutput exposes the installed list parsing with Name/Id/Version.
func ParseInstalledListOutput(raw string) []models.InstalledPackage {
	return parseInstalledListOutput(raw)
}

// GetInstalledPackages lista os apps instalados reconhecidos pelo winget com
// Name/Id/Version. É o índice que liga o inventário de registro ao Id real do
// pacote no winget (o installId do registro costuma ser o ProductCode do MSI).
func (s *Service) GetInstalledPackages() ([]models.InstalledPackage, error) {
	ctx := context.Background()
	if s.ctx != nil {
		ctx = s.ctx()
	}
	raw, err := s.apps.ListInstalled(ctx)
	if err != nil {
		return nil, err
	}
	s.logf("[winget list] " + s.now().Format("15:04:05"))
	items := parseInstalledListOutput(raw)

	// Chocolatey: lista local (id|version) para correlacionar apps instalados
	// via choco. Falha é tolerada (choco ausente) e não derruba o winget.
	chocoRaw, chocoErr := s.apps.ListInstalledChocolatey(ctx)
	if chocoErr != nil {
		s.logf("[choco list] erro: " + chocoErr.Error())
	} else if strings.TrimSpace(chocoRaw) != "" {
		s.logf("[choco list] " + s.now().Format("15:04:05"))
		items = append(items, parseChocolateyListOutput(chocoRaw)...)
	}

	return mergeChocolateyNuspec(items), nil
}

// mergeChocolateyNuspec enriquece as entradas Chocolatey com o título de
// exibição lido do .nuspec local (o "choco list" só traz id|version) e adiciona
// pacotes que a listagem não retornou. É o elo local entre o nome do registro e
// o Id real do pacote choco, sem depender do catálogo da loja.
func mergeChocolateyNuspec(items []models.InstalledPackage) []models.InstalledPackage {
	packages := chocolatey.ScanInstalledPackages()
	if len(packages) == 0 {
		return items
	}

	byID := make(map[string]chocolatey.InstalledPackageInfo, len(packages))
	for _, info := range packages {
		byID[strings.ToLower(info.ID)] = info
	}

	seen := make(map[string]struct{}, len(packages))
	for i := range items {
		if !strings.EqualFold(items[i].Source, "chocolatey") {
			continue
		}
		key := strings.ToLower(items[i].ID)
		seen[key] = struct{}{}

		info, ok := byID[key]
		if !ok {
			continue
		}
		if info.Title != "" {
			items[i].Name = info.Title
		}
		if items[i].Version == "" {
			items[i].Version = info.Version
		}
	}

	for _, info := range packages {
		key := strings.ToLower(strings.TrimSpace(info.ID))
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		name := info.Title
		if name == "" {
			name = info.ID
		}
		items = append(items, models.InstalledPackage{
			Name:    name,
			ID:      info.ID,
			Version: info.Version,
			Source:  "chocolatey",
		})
		seen[key] = struct{}{}
	}

	return items
}

// parseChocolateyListOutput parseia a saída do "choco list --local-only
// --limit-output" (id|version por linha).
func parseChocolateyListOutput(raw string) []models.InstalledPackage {
	lines := strings.Split(raw, "\n")
	items := make([]models.InstalledPackage, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if line == "" || !strings.Contains(line, "|") {
			continue
		}
		parts := strings.SplitN(line, "|", 2)
		id := strings.TrimSpace(parts[0])
		if id == "" {
			continue
		}
		version := ""
		if len(parts) > 1 {
			version = strings.TrimSpace(parts[1])
		}
		items = append(items, models.InstalledPackage{
			Name:    id,
			ID:      id,
			Version: version,
			Source:  "chocolatey",
		})
	}
	return items
}

// collapseTableLines simula a sobrescrita de linha por CR do winget (spinners
// usam \r sem \n): para cada linha \n-terminada mantém o último segmento \r
// não-vazio.
func collapseTableLines(raw string) []string {
	rawLines := strings.Split(raw, "\n")
	lines := make([]string, 0, len(rawLines))
	for _, l := range rawLines {
		parts := strings.Split(l, "\r")
		last := ""
		for j := len(parts) - 1; j >= 0; j-- {
			if strings.TrimSpace(parts[j]) != "" {
				last = parts[j]
				break
			}
		}
		lines = append(lines, last)
	}
	return lines
}

// parseInstalledListOutput parseia a tabela do "winget list" (Name, Id, Version,
// Available, Source) em itens com Name/Id/Version. As colunas são localizadas
// pelo cabeçalho (winget pode estar localizado).
func parseInstalledListOutput(raw string) []models.InstalledPackage {
	lines := collapseTableLines(raw)

	headerIdx := -1
	for i, line := range lines {
		lower := strings.ToLower(line)
		if (strings.Contains(lower, "name") || strings.Contains(lower, "nome")) &&
			strings.Contains(lower, "id") &&
			(strings.Contains(lower, "version") || strings.Contains(lower, "vers")) {
			headerIdx = i
			break
		}
	}
	if headerIdx < 0 || headerIdx+1 >= len(lines) {
		return nil
	}

	dataStart := headerIdx + 1
	if dataStart < len(lines) && strings.Count(lines[dataStart], "-") > 10 {
		dataStart++
	}

	header := lines[headerIdx]
	idCol := findColumnStart(header, "Id")
	if idCol < 0 {
		idCol = findColumnStart(header, "ID")
	}
	verCol := findColumnStart(header, "Version")
	if verCol < 0 {
		verCol = findColumnStart(header, "Vers")
	}
	if idCol <= 0 || verCol <= idCol {
		return nil
	}

	availCol := findColumnStart(header, "Available")
	if availCol < 0 {
		availCol = findColumnStart(header, "Dispon")
	}
	srcCol := findColumnStart(header, "Source")
	if srcCol < 0 {
		srcCol = findColumnStart(header, "Origem")
	}

	versionEnd := len(header)
	for _, candidate := range []int{availCol, srcCol} {
		if candidate > verCol && candidate < versionEnd {
			versionEnd = candidate
		}
	}

	items := make([]models.InstalledPackage, 0, len(lines)-dataStart)
	for _, line := range lines[dataStart:] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		name := strings.TrimSpace(safeSubstring(line, 0, idCol))
		id := collapseSpaces(safeSubstring(line, idCol, verCol))
		if name == "" || id == "" {
			continue
		}
		version := collapseSpaces(safeSubstring(line, verCol, versionEnd))
		items = append(items, models.InstalledPackage{Name: name, ID: id, Version: version, Source: "winget"})
	}
	return items
}

// GetPackageActions returns a contextual action map keyed by package id.
// Values: install, uninstall, upgrade.
func (s *Service) GetPackageActions() (map[string]string, error) {
	done := s.beginActivity("contexto de pacotes")
	if done != nil {
		defer done()
	}

	actions := map[string]string{}
	ctx := context.Background()
	if s.ctx != nil {
		ctx = s.ctx()
	}

	installedRaw, err := s.apps.ListInstalled(ctx)
	if err != nil {
		return actions, err
	}
	s.logf("[winget list] " + s.now().Format("15:04:05"))
	s.logf(installedRaw)

	for _, id := range parseInstalledOutput(installedRaw) {
		actions[strings.ToLower(id)] = packageActionUninstall
	}

	updatesRaw, updatesErr := s.apps.ListUpgradable(ctx)
	s.logf("[winget upgrade] " + s.now().Format("15:04:05"))
	s.logf(updatesRaw)
	if updatesErr == nil {
		for _, u := range parseUpgradeOutput(updatesRaw) {
			if strings.TrimSpace(u.ID) == "" {
				continue
			}
			actions[strings.ToLower(u.ID)] = packageActionUpgrade
		}
	}

	// Updates do Chocolatey também habilitam o "Atualizar" no card da loja.
	if s.apps != nil {
		chocoRaw, chocoErr := s.apps.ListUpgradableChocolatey(ctx)
		if chocoErr != nil {
			s.logf("[choco outdated] erro: " + chocoErr.Error())
		} else {
			for _, u := range parseChocolateyOutdatedOutput(chocoRaw) {
				if strings.TrimSpace(u.ID) == "" {
					continue
				}
				actions[strings.ToLower(u.ID)] = packageActionUpgrade
			}
		}
	}

	return actions, nil
}

// parseUpgradeOutput parses the tabular output of `winget upgrade`.
func parseUpgradeOutput(raw string) []models.UpgradeItem {
	lines := collapseTableLines(raw)

	var items []models.UpgradeItem
	headerIdx := -1

	// Find the header line (contains "Name" and "Id" and "Version")
	for i, line := range lines {
		lower := strings.ToLower(line)
		if (strings.Contains(lower, "name") || strings.Contains(lower, "nome")) &&
			(strings.Contains(lower, "id")) &&
			(strings.Contains(lower, "version") || strings.Contains(lower, "vers")) {
			headerIdx = i
			break
		}
	}
	if headerIdx < 0 || headerIdx+1 >= len(lines) {
		return items
	}

	// Find the separator line (dashes)
	dataStart := headerIdx + 1
	if dataStart < len(lines) && strings.Count(lines[dataStart], "-") > 10 {
		dataStart++
	}

	// Parse column positions from header
	header := lines[headerIdx]
	idCol := findColumnStart(header, "Id")
	if idCol < 0 {
		idCol = findColumnStart(header, "ID")
	}
	verCol := findColumnStart(header, "Version")
	if verCol < 0 {
		verCol = findColumnStart(header, "Vers")
	}
	availCol := findColumnStart(header, "Available")
	if availCol < 0 {
		availCol = findColumnStart(header, "Dispon")
	}
	srcCol := findColumnStart(header, "Source")
	if srcCol < 0 {
		// winget localizado: pt-BR "Origem", es "Origen".
		srcCol = findColumnStart(header, "Origem")
	}
	if srcCol < 0 {
		srcCol = findColumnStart(header, "Origen")
	}
	if srcCol < 0 {
		srcCol = findColumnStart(header, "Fonte")
	}

	for _, line := range lines[dataStart:] {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		// Skip summary lines like "X upgrades available"
		lower := strings.ToLower(line)
		if strings.Contains(lower, "upgrade") || strings.Contains(lower, "atualiza") {
			continue
		}

		item := models.UpgradeItem{}
		if idCol > 0 {
			item.Name = strings.TrimSpace(safeSubstring(line, 0, idCol))
		}
		if idCol >= 0 && verCol > idCol {
			item.ID = collapseSpaces(safeSubstring(line, idCol, verCol))
		}
		if verCol >= 0 && availCol > verCol {
			item.CurrentVersion = collapseSpaces(safeSubstring(line, verCol, availCol))
		}
		if availCol >= 0 {
			if srcCol > availCol {
				item.AvailableVersion = collapseSpaces(safeSubstring(line, availCol, srcCol))
			} else {
				// Sem coluna de origem reconhecida no cabeçalho: o restante da
				// linha pode trazer a origem grudada na versão ("26.03   winget").
				item.AvailableVersion, item.Source = splitTrailingSource(safeSubstring(line, availCol, len(line)))
			}
		}
		if srcCol >= 0 {
			item.Source = collapseSpaces(safeSubstring(line, srcCol, len(line)))
		}

		if item.ID != "" {
			items = append(items, item)
		}
	}
	return items
}

func parseChocolateyOutdatedOutput(raw string) []models.UpgradeItem {
	lines := strings.Split(raw, "\n")
	items := make([]models.UpgradeItem, 0, len(lines))

	for _, line := range lines {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if line == "" || !strings.Contains(line, "|") {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) < 3 {
			continue
		}

		id := strings.TrimSpace(parts[0])
		currentVersion := strings.TrimSpace(parts[1])
		availableVersion := strings.TrimSpace(parts[2])
		if id == "" || availableVersion == "" {
			continue
		}

		item := models.UpgradeItem{
			Name:             id,
			ID:               id,
			CurrentVersion:   currentVersion,
			AvailableVersion: availableVersion,
			Source:           "chocolatey",
		}
		items = append(items, item)
	}

	return items
}

func dedupeUpgradeItems(items []models.UpgradeItem) []models.UpgradeItem {
	if len(items) == 0 {
		return items
	}

	seen := make(map[string]struct{}, len(items))
	filtered := make([]models.UpgradeItem, 0, len(items))
	for _, item := range items {
		source := normalizeUpgradeSource(item.Source)
		if source == "" {
			source = "winget"
		}
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		key := strings.ToLower(source + "|" + id)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		item.Source = source
		filtered = append(filtered, item)
	}
	return filtered
}

func normalizeUpgradeSource(raw string) string {
	source := strings.ToLower(strings.TrimSpace(raw))
	switch source {
	case "choco", "chocolatey":
		return "chocolatey"
	case "winget":
		return "winget"
	default:
		return source
	}
}

// findColumnStart localiza o início da coluna do keyword no cabeçalho em
// RUNES (mesma unidade usada por safeSubstring). strings.Index retorna offset
// em BYTES: com cabeçalhos localizados ("Versão", "Disponível") cada caractere
// multi-byte antes da coluna desloca o índice em 1 e corrompe o fatiamento das
// linhas de dados — o 1º caractere da versão disponível vazava para a célula
// anterior e a origem vazava para a versão disponível.
func findColumnStart(header, keyword string) int {
	idx := strings.Index(header, keyword)
	if idx < 0 {
		idx = strings.Index(strings.ToLower(header), strings.ToLower(keyword))
	}
	if idx < 0 {
		return -1
	}
	return utf8.RuneCountInString(header[:idx])
}

// knownUpgradeSources lista os nomes de origem que winget/chocolatey imprimem
// na coluna Source/Origem (nomes de source não são localizados).
var knownUpgradeSources = map[string]struct{}{
	"winget":     {},
	"msstore":    {},
	"choco":      {},
	"chocolatey": {},
}

// splitTrailingSource separa um token de origem conhecido do fim do valor
// extraído quando a coluna de origem não foi localizada no cabeçalho.
func splitTrailingSource(tail string) (value, source string) {
	fields := strings.Fields(tail)
	if len(fields) > 1 {
		if _, ok := knownUpgradeSources[strings.ToLower(fields[len(fields)-1])]; ok {
			return strings.Join(fields[:len(fields)-1], " "), fields[len(fields)-1]
		}
	}
	return strings.TrimSpace(tail), ""
}

// collapseSpaces normaliza runs de espaços internos em campos que nunca
// contêm espaços duplos (proteção contra leve desalinhamento das colunas).
func collapseSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func safeSubstring(s string, start, end int) string {
	runes := []rune(s)
	if start >= len(runes) {
		return ""
	}
	if end > len(runes) {
		end = len(runes)
	}
	if start < 0 {
		start = 0
	}
	return string(runes[start:end])
}

func parseInstalledOutput(raw string) []string {
	lines := collapseTableLines(raw)

	headerIdx := -1
	for i, line := range lines {
		lower := strings.ToLower(line)
		if (strings.Contains(lower, "name") || strings.Contains(lower, "nome")) &&
			(strings.Contains(lower, "id")) &&
			(strings.Contains(lower, "version") || strings.Contains(lower, "vers")) {
			headerIdx = i
			break
		}
	}
	if headerIdx < 0 || headerIdx+1 >= len(lines) {
		return nil
	}

	dataStart := headerIdx + 1
	if dataStart < len(lines) && strings.Count(lines[dataStart], "-") > 10 {
		dataStart++
	}

	header := lines[headerIdx]
	idCol := findColumnStart(header, "Id")
	if idCol < 0 {
		idCol = findColumnStart(header, "ID")
	}
	verCol := findColumnStart(header, "Version")
	if verCol < 0 {
		verCol = findColumnStart(header, "Vers")
	}
	if idCol < 0 || verCol <= idCol {
		return nil
	}

	ids := make([]string, 0, len(lines)-dataStart)
	for _, line := range lines[dataStart:] {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		id := strings.TrimSpace(safeSubstring(line, idCol, verCol))
		if id == "" {
			continue
		}
		ids = append(ids, id)
	}
	return ids
}
