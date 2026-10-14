package inventory

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode"

	"discovery/app/appstore"
	"discovery/app/core/models"
	"discovery/app/core/processutil"
)

// customDetectionCommandTimeout limita a execução do comando de detecção de um
// pacote custom. A detecção roda DENTRO de caminhos de decisão (automação/policy
// sync e o portão antes do prompt), então precisa de um teto curto: um script
// travado bloqueava o portão por até 2 minutos. Estourar o teto é fail-safe —
// devolve "não instalado" e a instalação manual/automática segue.
const customDetectionCommandTimeout = 60 * time.Second

// customDetectionLogf registra uma linha sem panic quando o service foi criado
// sem logger (testes/uso isolado da detecção).
func (s *Service) customDetectionLogf(format string, args ...any) {
	if s == nil || s.logf == nil {
		return
	}
	s.logf(fmt.Sprintf(format, args...))
}

// customDetectionContext devolve o contexto do service sem panics quando ele
// não foi configurado (testes/uso isolado da detecção).
func (s *Service) customDetectionContext() context.Context {
	if s == nil || s.ctx == nil {
		return context.Background()
	}
	if ctx := s.ctx(); ctx != nil {
		return ctx
	}
	return context.Background()
}

// IsCustomPackageInstalled informa se um pacote custom está instalado no host,
// segundo o DetectionMode configurado no servidor (contrato §1.3):
//
//   - None (0) / campos vazios → (false, "") — sem evidência, fail-safe.
//   - InventoryLink (1) → casa o app no inventário LOCAL por nome normalizado
//     (sem acento) e, quando informado, fabricante; com DetectionMinVersion a
//     versão instalada precisa ser >= a mínima.
//   - Command (2) → executa DetectionCommand em PowerShell; exit 0 = instalado.
//
// Falha de execução/coleta NUNCA é tratada como instalado: devolve
// (false, motivo) para que a instalação manual siga normalmente.
func (s *Service) IsCustomPackageInstalled(ctx context.Context, item appstore.Item) (bool, string) {
	mode := item.DetectionMode
	name := strings.TrimSpace(item.DetectionName)
	command := strings.TrimSpace(item.DetectionCommand)

	switch mode {
	case appstore.DetectionModeInventoryLink:
		if name == "" {
			return false, "detecção por inventário sem nome configurado"
		}
		return s.detectByInventory(ctx, item)
	case appstore.DetectionModeCommand:
		if command == "" {
			return false, "detecção por comando vazia"
		}
		return runCustomDetectionCommand(ctx, command)
	default:
		return false, ""
	}
}

// detectByInventory coleta o inventário local (mesmo caminho do
// RefreshSoftware: CollectSoftware do provider) e casa o app configurado.
func (s *Service) detectByInventory(ctx context.Context, item appstore.Item) (bool, string) {
	if s == nil || s.inventory == nil {
		return false, "inventário indisponível"
	}

	software, err := s.inventory.CollectSoftware(ctx)
	if err != nil {
		return false, fmt.Sprintf("falha ao coletar inventário local: %v", err)
	}

	installed, detail := findInstalledSoftware(software, item.DetectionName, item.DetectionPublisher, item.DetectionMinVersion)
	if !installed {
		return false, ""
	}
	return true, detail
}

// runCustomDetectionCommand executa o PowerShell de detecção. Exit 0 é a única
// evidência positiva de instalação (contrato §1.3).
func runCustomDetectionCommand(ctx context.Context, command string) (bool, string) {
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithTimeout(ctx, customDetectionCommandTimeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", command)
	processutil.HideWindow(cmd)
	output, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(output))

	if err == nil {
		return true, "comando de detecção retornou exit 0 (instalado)"
	}
	if runCtx.Err() == context.DeadlineExceeded {
		return false, "comando de detecção excedeu o tempo limite"
	}
	if text != "" {
		return false, fmt.Sprintf("comando de detecção não indicou instalação (%v): %s", err, truncateDetectionDetail(text))
	}
	return false, fmt.Sprintf("comando de detecção não indicou instalação (%v)", err)
}

// MarkCatalogInstalled preenche AppItem.Installed dos itens Custom cuja
// detecção é InventoryLink, usando UMA única coleta do inventário local por
// montagem do catálogo (o comando de detecção NÃO roda na listagem — é caro).
//
// Fail-safe: qualquer erro de coleta deixa Installed=false e o catálogo segue.
func (s *Service) MarkCatalogInstalled(ctx context.Context, catalog models.Catalog) models.Catalog {
	if s == nil || s.inventory == nil || len(catalog.Packages) == 0 {
		return catalog
	}
	if !catalogNeedsInventoryDetection(catalog) {
		return catalog
	}

	software, err := s.inventory.CollectSoftware(ctx)
	if err != nil {
		s.customDetectionLogf("[store] aviso: deteccao por inventario indisponivel: %v", err)
		return catalog
	}

	software = append([]models.SoftwareItem(nil), software...)
	detector := newInventoryDetector(software)
	for i := range catalog.Packages {
		pkg := &catalog.Packages[i]
		if !isCustomInventoryLinkPackage(*pkg) {
			continue
		}
		installed, _ := detector.Match(pkg.DetectionName, pkg.DetectionPublisher, pkg.DetectionMinVersion)
		pkg.Installed = installed
	}
	return catalog
}

// catalogNeedsInventoryDetection diz se algum item exige a coleta do inventário
// local (evita coletar quando nenhum pacote custom usa InventoryLink).
func catalogNeedsInventoryDetection(catalog models.Catalog) bool {
	for i := range catalog.Packages {
		if isCustomInventoryLinkPackage(catalog.Packages[i]) {
			return true
		}
	}
	return false
}

func isCustomInventoryLinkPackage(pkg models.AppItem) bool {
	if !strings.EqualFold(strings.TrimSpace(pkg.InstallationType), string(appstore.InstallationCustom)) {
		return false
	}
	return pkg.DetectionMode == appstore.DetectionModeInventoryLink &&
		strings.TrimSpace(pkg.DetectionName) != ""
}

// KnownCustomPackageInstalled é o checker injetado na automação
// (automation.SetCustomInstalledChecker). Informa se o pacote custom está
// instalado segundo a detecção configurada no servidor:
//
//   - DetectionMode InventoryLink → usa o Installed já calculado na montagem do
//     catálogo da loja (que faz UMA coleta do inventário por listagem). Sem
//     catálogo montado ainda, devolve known=false (fail-safe: não pula).
//   - DetectionMode Command → executa o comando de detecção (exit 0 = instalado).
//   - None/sem evidência → known=false.
//
// Nunca devolve installed=true sem evidência positiva.
func (s *Service) KnownCustomPackageInstalled(installationType, packageID string) (known bool, installed bool) {
	target := strings.TrimSpace(packageID)
	if s == nil || target == "" {
		return false, false
	}

	item, err := s.cachedCustomCatalogItem(installationType, target)
	if err != nil || item == nil {
		return false, false
	}

	switch item.DetectionMode {
	case appstore.DetectionModeInventoryLink:
		if strings.TrimSpace(item.DetectionName) == "" {
			return false, false
		}
		return true, item.Installed
	case appstore.DetectionModeCommand:
		detection := detectionItemFromAppItem(*item)
		if strings.TrimSpace(detection.DetectionCommand) == "" {
			return false, false
		}
		installed, detail := s.IsCustomPackageInstalled(s.customDetectionContext(), detection)
		if strings.TrimSpace(detail) != "" {
			s.customDetectionLogf("[automation] deteccao do custom %s: %s", target, detail)
		}
		return true, installed
	default:
		return false, false
	}
}

// detectionItemFromAppItem reconstrói o mínimo de appstore.Item necessário para
// rodar a detecção a partir do DTO da UI (o catálogo montado não guarda o
// appstore.Item original).
func detectionItemFromAppItem(pkg models.AppItem) appstore.Item {
	return appstore.Item{
		InstallationType:     strings.TrimSpace(pkg.InstallationType),
		PackageID:            strings.TrimSpace(pkg.ID),
		DetectionMode:        pkg.DetectionMode,
		InventorySoftwareID:  strings.TrimSpace(pkg.InventorySoftwareID),
		DetectionName:        strings.TrimSpace(pkg.DetectionName),
		DetectionPublisher:   strings.TrimSpace(pkg.DetectionPublisher),
		DetectionMinVersion:  strings.TrimSpace(pkg.DetectionMinVersion),
		DetectionCommand:     strings.TrimSpace(pkg.DetectionCommand),
		InstallCommandKind:   pkg.InstallCommandKind,
		UninstallCommandKind: pkg.UninstallCommandKind,
	}
}

// cachedCustomCatalogItem localiza o item custom no catálogo já montado, sem
// forçar um novo fetch da loja (a automação roda em background e não deve
// disparar rede a cada avaliação de tarefa).
func (s *Service) cachedCustomCatalogItem(installationType, packageID string) (*models.AppItem, error) {
	if s.getCatalog == nil {
		return nil, fmt.Errorf("catálogo indisponível")
	}
	catalog, err := s.getCatalog(s.customDetectionContext())
	if err != nil {
		return nil, err
	}

	wantType := strings.TrimSpace(installationType)
	for i := range catalog.Packages {
		pkg := catalog.Packages[i]
		if !strings.EqualFold(strings.TrimSpace(pkg.ID), packageID) {
			continue
		}
		if wantType != "" && !strings.EqualFold(strings.TrimSpace(pkg.InstallationType), wantType) {
			continue
		}
		copy := pkg
		return &copy, nil
	}
	return nil, nil
}

// inventoryDetector casa itens do catálogo contra a lista de softwares
// coletada. As funções puras (matchSoftwareByName, compareVersionsLoose,
// normalizeDetectionText) ficam separadas para teste unitário.
type inventoryDetector struct {
	software []models.SoftwareItem
	byName   map[string][]models.SoftwareItem
}

func newInventoryDetector(software []models.SoftwareItem) *inventoryDetector {
	d := &inventoryDetector{
		software: software,
		byName:   make(map[string][]models.SoftwareItem, len(software)),
	}
	for _, sw := range software {
		key := normalizeDetectionText(sw.Name)
		if key == "" {
			continue
		}
		d.byName[key] = append(d.byName[key], sw)
	}
	return d
}

// Match procura nas linhas de mesmo nome normalizado uma que satisfaça
// fabricante (quando informado) e versão mínima (quando informada).
func (d *inventoryDetector) Match(name, publisher, minVersion string) (bool, string) {
	if d == nil {
		return false, ""
	}
	key := normalizeDetectionText(name)
	if key == "" {
		return false, ""
	}

	candidates := d.byName[key]
	for _, sw := range candidates {
		if !matchSoftwareByName(name, sw.Name) {
			continue
		}
		if !matchSoftwarePublisher(publisher, sw.Publisher) {
			continue
		}
		if !matchSoftwareMinVersion(minVersion, sw.Version) {
			continue
		}
		detail := strings.TrimSpace(sw.Name)
		if v := strings.TrimSpace(sw.Version); v != "" {
			detail += " " + v
		}
		return true, strings.TrimSpace(detail)
	}
	return false, ""
}

// matchSoftwareByName compara o nome de detecção com o nome do software do
// inventário usando o nome normalizado (trim + lower + sem acento + espaços
// colapsados).
func matchSoftwareByName(detectionName, softwareName string) bool {
	want := normalizeDetectionText(detectionName)
	if want == "" {
		return false
	}
	return want == normalizeDetectionText(softwareName)
}

// matchSoftwarePublisher exige fabricante quando informado; fabricante vazio na
// configuração aceita qualquer publisher (contrato §1.3).
func matchSoftwarePublisher(detectionPublisher, softwarePublisher string) bool {
	want := normalizeDetectionText(detectionPublisher)
	if want == "" {
		return true
	}
	got := normalizeDetectionText(softwarePublisher)
	if got == "" {
		return false
	}
	return strings.Contains(got, want) || strings.Contains(want, got)
}

// matchSoftwareMinVersion aceita qualquer versão quando a mínima não é
// informada ou não é comparável (fail-safe: sem evidência de que NÃO atende).
func matchSoftwareMinVersion(minVersion, installedVersion string) bool {
	if strings.TrimSpace(minVersion) == "" {
		return true
	}
	installed := strings.TrimSpace(installedVersion)
	if installed == "" {
		return false
	}
	cmp, ok := compareVersionsLoose(installed, minVersion)
	if !ok {
		return true
	}
	return cmp >= 0
}

// findInstalledSoftware é a versão "one-shot" usada pela detecção sob demanda
// (IsCustomPackageInstalled): monta o índice e casa uma vez.
func findInstalledSoftware(software []models.SoftwareItem, name, publisher, minVersion string) (bool, string) {
	return newInventoryDetector(software).Match(name, publisher, minVersion)
}

// compareVersionsLoose compara duas versões tolerando prefixos/sufixos textuais
// ("v1.2.3", "1.2.3-beta", "2026.2"). Devolve (comparação, ok): -1 menor,
// 0 igual, 1 maior. ok=false quando nenhum número pôde ser extraído de um dos
// lados — o chamador decide o fallback.
func compareVersionsLoose(a, b string) (int, bool) {
	left, okLeft := versionNumbers(a)
	right, okRight := versionNumbers(b)
	if !okLeft || !okRight {
		return 0, false
	}

	max := len(left)
	if len(right) > max {
		max = len(right)
	}
	for i := 0; i < max; i++ {
		var lv, rv int
		if i < len(left) {
			lv = left[i]
		}
		if i < len(right) {
			rv = right[i]
		}
		if lv != rv {
			if lv < rv {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

// versionNumbers extrai a sequência de componentes numéricos de uma versão.
func versionNumbers(version string) ([]int, bool) {
	fields := strings.FieldsFunc(version, func(r rune) bool {
		return !unicode.IsDigit(r)
	})
	numbers := make([]int, 0, len(fields))
	for _, field := range fields {
		value, err := strconv.Atoi(field)
		if err != nil {
			continue
		}
		numbers = append(numbers, value)
	}
	if len(numbers) == 0 {
		return nil, false
	}
	return numbers, true
}

// normalizeDetectionText normaliza texto para comparação: trim, lower, sem
// acento e com espaços internos colapsados.
func normalizeDetectionText(value string) string {
	lowered := strings.ToLower(strings.TrimSpace(value))
	if lowered == "" {
		return ""
	}

	var builder strings.Builder
	builder.Grow(len(lowered))
	lastSpace := false
	for _, r := range lowered {
		r = stripDiacritic(r)
		if r == '\uFFFD' {
			continue
		}
		if unicode.IsSpace(r) {
			if lastSpace {
				continue
			}
			lastSpace = true
			builder.WriteRune(' ')
			continue
		}
		lastSpace = false
		builder.WriteRune(r)
	}
	return strings.TrimSpace(builder.String())
}

// stripDiacritic remove o acento das letras latinas usadas em português.
func stripDiacritic(r rune) rune {
	switch r {
	case 'á', 'à', 'â', 'ã', 'ä', 'å':
		return 'a'
	case 'é', 'è', 'ê', 'ë':
		return 'e'
	case 'í', 'ì', 'î', 'ï':
		return 'i'
	case 'ó', 'ò', 'ô', 'õ', 'ö':
		return 'o'
	case 'ú', 'ù', 'û', 'ü':
		return 'u'
	case 'ç':
		return 'c'
	case 'ñ':
		return 'n'
	case 'ý', 'ÿ':
		return 'y'
	}
	return r
}

// truncateDetectionDetail limita o texto do motivo devolvido à UI/log.
func truncateDetectionDetail(text string) string {
	const limit = 300
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "... (truncado)"
}
