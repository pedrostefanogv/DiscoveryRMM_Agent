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

// MarkCatalogInstalled preenche AppItem.Installed dos itens da loja:
//
//   - Custom com detecção InventoryLink → casa o app no inventário LOCAL por
//     nome normalizado (comportamento original, preservado).
//   - Winget / Chocolatey → casa o PackageID do catálogo contra os pacotes
//     reconhecidos pelo gerenciador (`winget list` / `choco list`, via
//     Service.InstalledPackages), com fallback por nome de exibição.
//
// Cada fonte é coletada NO MÁXIMO uma vez por montagem do catálogo, e só quando
// o catálogo realmente tem itens que dependem dela (ver catalogNeedsInventoryDetection
// e catalogNeedsInstalledPackages): o comando de detecção custom NÃO roda na
// listagem — é caro.
//
// Fail-safe: qualquer erro de coleta deixa os itens dependentes dessa fonte com
// Installed=false e o catálogo segue; a listagem da loja nunca falha por isso.
func (s *Service) MarkCatalogInstalled(ctx context.Context, catalog models.Catalog) models.Catalog {
	if s == nil || len(catalog.Packages) == 0 {
		return catalog
	}

	if s.inventory != nil && catalogNeedsInventoryDetection(catalog) {
		software, err := s.inventory.CollectSoftware(ctx)
		if err != nil {
			s.customDetectionLogf("[store] aviso: deteccao por inventario indisponivel: %v", err)
		} else {
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
		}
	}

	if len(catalog.Packages) > 0 && catalogNeedsInstalledPackages(catalog) {
		s.markMarketplacePackagesInstalled(ctx, &catalog)
	}

	return catalog
}

// markMarketplacePackagesInstalled preenche Installed dos itens Winget/Chocolatey
// a partir da lista de pacotes instalados reconhecidos pelo gerenciador.
//
// Fail-safe absoluto: erro/timeout na coleta loga um aviso e deixa TODOS os
// itens como não instalados — a loja continua listando normalmente.
func (s *Service) markMarketplacePackagesInstalled(ctx context.Context, catalog *models.Catalog) {
	if s == nil || catalog == nil {
		return
	}
	if s.installedPackages == nil {
		// Sem a fonte injetada (uso isolado do service): nada a marcar.
		return
	}

	// Usa o cache de "winget list" do service (installedPackagesCacheTTL, 10min):
	// a loja é remontada a cada abertura, e rodar "winget list" a cada montagem
	// (segundos de processo) travaria a listagem. loadInstalledPackages ainda
	// reutiliza a última lista boa em falha transitória.
	items := s.loadInstalledPackages(ctx)
	if len(items) == 0 {
		s.customDetectionLogf("[store] aviso: lista de pacotes instalados vazia; itens da loja ficam como nao instalados")
		return
	}

	index := newStoreInstalledIndex(items)
	marked := 0
	for i := range catalog.Packages {
		pkg := &catalog.Packages[i]
		if !isMarketplacePackage(*pkg) {
			continue
		}
		ok, detail := index.match(*pkg)
		pkg.Installed = ok
		if !ok {
			if detail != "" {
				// Ambiguidade/guarda de variante: registra o motivo para
				// diagnóstico sem transformar a dúvida em evidência.
				s.customDetectionLogf("[store] %s (%s): %s", strings.TrimSpace(pkg.ID), strings.TrimSpace(pkg.InstallationType), detail)
			}
			continue
		}
		marked++
		if detail != "" {
			s.customDetectionLogf("[store] %s marcado como instalado por ID exato (%s)", strings.TrimSpace(pkg.ID), detail)
		}
	}
	if marked > 0 {
		s.customDetectionLogf("[store] %d item(ns) da loja marcados como instalados", marked)
	}
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

// catalogNeedsInstalledPackages diz se algum item exige a lista de pacotes
// instalados do gerenciador (winget/choco) para resolver Installed.
func catalogNeedsInstalledPackages(catalog models.Catalog) bool {
	for i := range catalog.Packages {
		if isMarketplacePackage(catalog.Packages[i]) {
			return true
		}
	}
	return false
}

// isMarketplacePackage identifica itens de origem Winget/Chocolatey — os que são
// instalados pelo gerenciador de pacotes e por isso podem ser confirmados pela
// lista de instalados (`winget list` / `choco list`).
func isMarketplacePackage(pkg models.AppItem) bool {
	switch strings.TrimSpace(pkg.InstallationType) {
	case string(appstore.InstallationWinget), string(appstore.InstallationChocolatey):
		return strings.TrimSpace(pkg.ID) != ""
	default:
		return false
	}
}

func isCustomInventoryLinkPackage(pkg models.AppItem) bool {
	if !strings.EqualFold(strings.TrimSpace(pkg.InstallationType), string(appstore.InstallationCustom)) {
		return false
	}
	return pkg.DetectionMode == appstore.DetectionModeInventoryLink &&
		strings.TrimSpace(pkg.DetectionName) != ""
}

// storeInstalledIndex é o índice dos pacotes instalados reconhecidos pelo
// gerenciador, usado para resolver Installed dos itens Winget/Chocolatey da loja.
//
// (Não confundir com installedPackageIndex, de sync.go, que indexa por nome para
// resolver o InstallID do inventário: este indexa por Id E por nome.)
type storeInstalledIndex struct {
	byID   map[string]models.InstalledPackage
	byName map[string][]models.InstalledPackage
}

func newStoreInstalledIndex(items []models.InstalledPackage) storeInstalledIndex {
	index := storeInstalledIndex{
		byID:   make(map[string]models.InstalledPackage, len(items)),
		byName: make(map[string][]models.InstalledPackage, len(items)),
	}
	for _, pkg := range items {
		// O ID do catálogo (`google.chrome.exe`) e o devolvido pela listagem
		// (`Google.Chrome.EXE`) divergem em caixa: a chave é sempre normalizada.
		if key := normalizePackageIDKey(pkg.ID); key != "" {
			if _, exists := index.byID[key]; !exists {
				index.byID[key] = pkg
			}
		}
		if key := canonicalProductNameKey(pkg.Name); key != "" {
			index.byName[key] = append(index.byName[key], pkg)
		}
	}
	return index
}

// match resolve um item do catálogo em três degraus de confiança decrescente:
//
//  1. ID exato (case-insensitive/trim) — alta precisão.
//  2. Nome normalizado, com GUARDA DE VARIANTE (stable ≠ Beta/Canary/ESR) e
//     desempate por versão; havendo mais de um candidato distinto, é ambíguo e
//     o item NÃO é marcado (fail-safe).
//
// Reusa a normalização de nome sem acento já existente (normalizeDetectionText),
// a mesma da detecção custom.
//
// ATENÇÃO: o `winget list` SEM filtro devolve, para apps instalados via ARP, o
// ID de ARP ("ARP\Machine\X86\Google Chrome") em vez do Id do pacote
// ("Google.Chrome.EXE") — por isso o degrau 2 é indispensável: só por ID o caso
// mais comum da loja nunca casaria.
//
// O segundo retorno é um detalhe de diagnóstico (motivo do não-casamento ou
// evidência do casamento); nunca é usado como evidência de instalação.
func (index storeInstalledIndex) match(pkg models.AppItem) (bool, string) {
	if entry, ok := index.byID[normalizePackageIDKey(pkg.ID)]; ok {
		return true, describeInstalledEntry(entry)
	}

	// O casamento por nome é um sinal FRACO: nomes genéricos de instalador
	// ("Setup", "Driver", "Update", "Runtime") aparecem em ARP de produtos
	// completamente diferentes e marcariam itens não instalados como
	// "Já instalado" — o mesmo bug relatado, invertido. Só aceitamos o degrau 2
	// quando o nome do PRODUTO é específico o bastante para identificar um app.
	catalogName := canonicalProductNameKey(pkg.Name)
	if !isSpecificProductName(catalogName) {
		return false, ""
	}
	candidates := index.byName[catalogName]
	if len(candidates) == 0 {
		return false, ""
	}

	viable := make([]models.InstalledPackage, 0, len(candidates))
	for _, candidate := range candidates {
		if !sameProductVariant(pkg.Name, candidate.Name) {
			continue
		}
		// Guarda de idioma: "(pt-BR)" no catálogo não pode casar com uma
		// instalação de outro idioma só porque os parênteses foram removidos.
		if !sameLocale(pkg.Name, candidate.Name) {
			continue
		}
		viable = append(viable, candidate)
	}
	if len(viable) == 0 {
		return false, fmt.Sprintf("nome %q casa apenas com variante diferente (%s); nao marcado", catalogName, describeInstalledEntries(candidates))
	}

	// Desempate por versão do catálogo: entre homônimos legítimos, a instalação
	// com a versão exata é a evidência correta.
	if want := normalizeDetectionText(pkg.Version); want != "" {
		for _, candidate := range viable {
			if normalizeDetectionText(candidate.Version) == want {
				return true, describeInstalledEntry(candidate) + " (versao exata)"
			}
		}
	}

	// Várias linhas distintas (ex.: Google Chrome x86 e x64) sem versão para
	// desempatar: a evidência é inconclusiva, então não marcamos.
	if distinctInstalledEntries(viable) > 1 {
		return false, fmt.Sprintf("nome %q ambiguo entre %d instalacoes sem desempate por versao; nao marcado (%s)", catalogName, distinctInstalledEntries(viable), describeInstalledEntries(viable))
	}

	return true, describeInstalledEntry(viable[0]) + " (casado por nome)"
}

// normalizePackageIDKey normaliza um Id de pacote para comparação: trim + lower.
// (`google.chrome.exe` == `Google.Chrome.EXE`).
func normalizePackageIDKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// localeTagNamePattern reconhece um segmento entre parênteses que é um TAG DE
// IDIOMA/REGIÃO (pt-BR, en-US, x64 pt-BR, es, fr-FR...). É usado para NÃO deixar
// um item de catálogo localizado casar com uma instalação de outro idioma.
func parenthesisedSegments(value string) []string {
	normalized := normalizeDetectionText(value)
	if normalized == "" {
		return nil
	}
	var segments []string
	depth := 0
	var current strings.Builder
	for _, r := range normalized {
		switch r {
		case '(', '[':
			depth++
			if depth == 1 {
				current.Reset()
			}
			continue
		case ')', ']':
			if depth > 0 {
				depth--
				if depth == 0 {
					if seg := strings.TrimSpace(current.String()); seg != "" {
						segments = append(segments, seg)
					}
				}
			}
			continue
		}
		if depth > 0 {
			current.WriteRune(r)
		}
	}
	return segments
}

// localeTokensFromSegments extrai tokens que parecem idioma/região de um segmento
// entre parênteses: "pt-br", "x64 pt-br", "en-us", "fr". Tokens puramente de
// arquitetura/empacotamento ("exe", "x64", "user", "machine") são ignorados.
func localeTokensFromSegments(segments []string) []string {
	var tokens []string
	for _, seg := range segments {
		for _, field := range strings.Fields(seg) {
			if isLocaleLikeToken(field) {
				tokens = append(tokens, field)
			}
		}
	}
	return tokens
}

// isLocaleLikeToken reconhece "pt", "pt-br", "en-us", "es", "fr-fr": duas letras,
// opcionalmente seguidas de hífen e duas letras. Descartando os tokens de
// arquitetura/empacotamento conhecidos.
func isLocaleLikeToken(token string) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	switch token {
	case "exe", "msi", "x64", "x86", "x32", "arm64", "user", "machine",
		"installer", "setup", "portable", "silent", "stable":
		return false
	}
	parts := strings.Split(token, "-")
	if len(parts) > 2 {
		return false
	}
	for _, part := range parts {
		if len(part) != 2 && len(part) != 3 {
			return false
		}
		for _, r := range part {
			if r < 'a' || r > 'z' {
				return false
			}
		}
	}
	return true
}

// sameLocale evita casar um item de catálogo LOCALIZADO com uma instalação de
// OUTRO idioma. Ausência de tag de idioma em um dos lados é aceita (o winget
// frequentemente omite); tags presentes e DIFERENTES bloqueiam o match.
func sameLocale(catalogName, installedName string) bool {
	catalogLocales := localeTokensFromSegments(parenthesisedSegments(catalogName))
	installedLocales := localeTokensFromSegments(parenthesisedSegments(installedName))

	if len(catalogLocales) == 0 || len(installedLocales) == 0 {
		return true
	}
	for _, want := range catalogLocales {
		for _, got := range installedLocales {
			if want == got || strings.HasPrefix(got, want+"-") || strings.HasPrefix(want, got+"-") {
				return true
			}
		}
	}
	return false
}

// genericProductNames são nomes de exibição que NÃO identificam um produto:
// aparecem em registros ARP de softwares distintos e não servem como evidência
// de instalação. A comparação é feita sobre o nome já canônico (minúsculo, sem
// acento, sem parênteses).
var genericProductNames = map[string]struct{}{
	"setup": {}, "installer": {}, "update": {}, "updates": {},
	"driver": {}, "drivers": {}, "runtime": {}, "redistributable": {},
	"redistributables": {}, "app": {}, "application": {}, "program": {},
	"software": {}, "tool": {}, "tools": {}, "utility": {}, "utilities": {},
	"client": {}, "server": {}, "service": {}, "agent": {}, "component": {},
	"library": {}, "libraries": {}, "package": {}, "patch": {}, "hotfix": {},
	"bundle": {}, "launcher": {}, "helper": {}, "microsoft": {}, "windows": {},
}

// isSpecificProductName diz se o nome canônico do produto é específico o
// suficiente para servir como evidência de instalação pelo nome.
//
// Exigências (todas):
//   - ao menos 2 tokens (um nome de produto real tem marca + produto);
//   - ao menos 8 caracteres no total;
//   - nenhum token isolado que seja nome genérico conhecido.
//
// "Google Chrome" passa; "Setup", "Driver", "Update", "Runtime" não passam.
func isSpecificProductName(canonicalName string) bool {
	name := strings.TrimSpace(canonicalName)
	if name == "" || len([]rune(name)) < 8 {
		return false
	}
	fields := strings.Fields(name)
	if len(fields) < 2 {
		return false
	}
	for _, field := range fields {
		if _, generic := genericProductNames[field]; generic {
			return false
		}
	}
	return true
}

// canonicalProductNameKey reduz um nome de exibição à chave do PRODUTO, para
// casar o nome do catálogo com o nome instalado mesmo quando um dos lados traz
// notação de empacotamento entre parênteses.
//
// Motivo (caso real): o catálogo da loja publica o Chrome como
// "Google Chrome (EXE)" enquanto o `winget list` devolve "Google Chrome" — o
// nome normalizado cru não casava e o card ficava preso em "Instalar".
//
// Regras:
//   - aplica normalizeDetectionText (trim + lower + sem acento);
//   - remove segmentos entre parênteses/colchetes, que no catálogo winget são
//     ARQUITETURA/EMPACOTAMENTO ("(EXE)", "(x64)", "(User)"), não identidade do
//     produto.
//
// As variantes REAIS (Beta/Canary/ESR/...) NÃO são removidas: elas aparecem como
// palavras soltas no nome e continuam sendo separadas por sameProductVariant.
func canonicalProductNameKey(value string) string {
	normalized := normalizeDetectionText(value)
	if normalized == "" {
		return ""
	}

	var builder strings.Builder
	builder.Grow(len(normalized))
	depth := 0
	for _, r := range normalized {
		switch r {
		case '(', '[':
			depth++
			continue
		case ')', ']':
			if depth > 0 {
				depth--
			}
			continue
		}
		if depth > 0 {
			continue
		}
		builder.WriteRune(r)
	}

	return normalizeDetectionText(builder.String())
}

// installedProductVariantSuffixes são segmentos que marcam uma VARIANTE do
// mesmo produto (canal de distribuição ou empacotamento). "Google Chrome" e
// "Google Chrome Beta" são produtos distintos: não podem ser tratados como a
// mesma instalação.
// Nota: "exe" NÃO entra nesta lista. Em nomes de exibição do winget ele é
// notação de EMPACOTAMENTO ("Google Chrome (EXE)", "Mozilla Firefox (EXE)"),
// não uma variante do produto: tratá-lo como variante rejeitava exatamente o
// caso "Google Chrome (EXE)" do catálogo contra "Google Chrome" instalado.
// Suportes de instalação (x64/x86/32-bit/64-bit) NÃO são variantes entre si:
// são o mesmo produto, então também ficam fora.
var installedProductVariantSuffixes = []string{
	"beta",
	"canary",
	"dev",
	"nightly",
	"esr",
	"preview",
	"insider",
	"insiders",
	"rc",
	"alpha",
	"lts",
	"next",
}

// sameProductVariant rejeita o match quando um dos nomes tem um segmento de
// variante que o outro não tem. Compara os nomes já normalizados
// (trim + lower + sem acento + espaços colapsados), de modo que
// "Google Chrome" × "Google Chrome (EXE)" casa, mas
// "Google Chrome" × "Google Chrome Beta" NÃO casa.
func sameProductVariant(a, b string) bool {
	left := normalizeDetectionText(a)
	right := normalizeDetectionText(b)
	if left == "" || right == "" {
		return false
	}
	for _, variant := range installedProductVariantSuffixes {
		if hasVariantSegment(left, variant) != hasVariantSegment(right, variant) {
			return false
		}
	}
	return true
}

// hasVariantSegment diz se o nome normalizado contém o segmento como palavra
// isolada ("chrome beta", "7-zip exe"), e não apenas como prefixo de outra
// palavra ("dev" em "developer").
func hasVariantSegment(name, variant string) bool {
	for _, field := range strings.FieldsFunc(name, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r))
	}) {
		if field == variant {
			return true
		}
	}
	return false
}

// distinctInstalledEntries conta linhas distintas (mesmo ID normalizado conta uma).
func distinctInstalledEntries(items []models.InstalledPackage) int {
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		key := normalizePackageIDKey(item.ID)
		if key == "" {
			key = normalizeDetectionText(item.Name) + "|" + normalizeDetectionText(item.Version)
		}
		seen[key] = struct{}{}
	}
	return len(seen)
}

func describeInstalledEntries(items []models.InstalledPackage) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, describeInstalledEntry(item))
	}
	return strings.Join(parts, "; ")
}

func describeInstalledEntry(item models.InstalledPackage) string {
	detail := strings.TrimSpace(item.Name)
	if detail == "" {
		detail = strings.TrimSpace(item.ID)
	}
	if id := strings.TrimSpace(item.ID); id != "" && !strings.EqualFold(id, detail) {
		detail += " [" + id + "]"
	}
	if version := strings.TrimSpace(item.Version); version != "" {
		detail += " " + version
	}
	return strings.TrimSpace(detail)
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
