// Package autoupdate implementa o auto-update da Loja no AGENTE (F2): a cada
// ciclo, cruza a política efetiva da app-store (itens aprovados com
// AutoUpdateEnabled) com a lista de updates pendentes (winget upgrade + choco
// outdated) e executa os upgrades via inventory.Service — que aplica o gate da
// Loja e a fila única de pacotes (F3).
//
// Regras de segurança:
//   - A precedência é da AUTOMAÇÃO: se uma task de policy da automação declara
//     o mesmo pacote (PackageAuthorizedByPolicy), o auto-update NÃO executa
//     (skip automated_by_task) — evita corrida entre o executor de tasks e o
//     auto-update.
//   - Janela de manutenção: com a janela ATIVA e agora FORA dela, nenhum
//     upgrade roda (skip outside_window). Com a janela DESLIGADA não há gate
//     (semântica igual ao deferral de power: janela off = sem restrição).
//   - Limite por ciclo (MaxPerCycle) e espaçamento mínimo entre execuções.
//   - Nunca reinicia o agente; reboot é responsabilidade da janela/policy.
package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"discovery/app/appstore"
	"discovery/app/core/maintenance"
	"discovery/app/core/models"
)

// TargetSeparator é o separador do target "source::id" consumido por
// inventory.Service.Upgrade (splitPackageManagerTarget). Formato idêntico ao
// usado pela UI em frontend/js/app-store-updates.js (buildUpdateUpgradeTarget).
const TargetSeparator = "::"

const (
	// MaxPerCycle limita quantos upgrades um único ciclo executa: com a
	// varredura a cada 6h, um host muito defasado converge gradualmente em vez
	// de monopolizar a fila de pacotes (e a banda) num único ciclo.
	MaxPerCycle = 3
	// MinSpacing é o espaçamento mínimo entre dois upgrades do MESMO ciclo:
	// dá tempo para o winget/choco liberar locks e para o refresh de
	// inventário pós-operação enfileirar sem tormenta.
	MinSpacing = 2 * time.Minute
	// RunInterval é o intervalo base entre ciclos.
	RunInterval = 6 * time.Hour
	// RunJitterMax é o jitter aleatório máximo aplicado ao intervalo (evita
	// que todos os agentes da frota batam no servidor no mesmo instante).
	RunJitterMax = 30 * time.Minute
	// StartDelay é o warm-up antes do primeiro RunOnce (deixa o provisionamento
	// e o primeiro policy-sync concluírem antes da primeira varredura).
	StartDelay = 5 * time.Minute
)

// Motivos de skip (contrato da tarefa F2; estáveis para diagnóstico).
const (
	SkipReasonAutomatedByTask = "automated_by_task"
	SkipReasonNoUpdate        = "no_update"
	SkipReasonDenied          = "denied"
	SkipReasonOutsideWindow   = "outside_window"
)

// PlanEntry é um upgrade aprovado para execução neste ciclo.
type PlanEntry struct {
	// Target é o "source::id" passado a inventory.Service.Upgrade.
	Target string
	// InstallationType/PackageID identificam o item da política efetiva.
	InstallationType string
	PackageID        string
	// Source é a origem NORMALIZADA do update pendente ("winget"/"chocolatey").
	Source string
	// AvailableVersion é a versão disponível no update pendente.
	AvailableVersion string
}

// AutoUpdateExecution é uma execução de auto-update registrada no ledger
// (F3): data, pacote, versão alvo e resultado. O agente grava localmente
// (SQLite) — o servidor NÃO recebe este histórico nesta etapa (decisão de
// menor risco: sem endpoint/tabela server-side neste ciclo).
type AutoUpdateExecution struct {
	// InstallationType/PackageID identificam o item da política efetiva.
	InstallationType string
	PackageID        string
	// Source é a origem NORMALIZADA do update pendente ("winget"/"chocolatey").
	Source string
	// Target é o "source::id" executado.
	Target string
	// TargetVersion é a versão alvo do upgrade (AvailableVersion do pendente).
	TargetVersion string
	// Result é "success" ou "error" (database.AutoUpdateResultSuccess/Error).
	Result string
	// ErrorMessage é o erro observado (vazio no sucesso).
	ErrorMessage string
	// ExecutedAt é o instante do resultado.
	ExecutedAt time.Time
}

// PlanSkip registra um item da política que NÃO vai rodar neste ciclo, com motivo.
type PlanSkip struct {
	InstallationType string
	PackageID        string
	Reason           string
}

// UpdatePlan é o resultado do Planner puro.
type UpdatePlan struct {
	// WindowBlocked indica que a janela de manutenção estava ativa e agora
	// (now) estava fora dela — TODOS os itens viraram skip outside_window.
	WindowBlocked bool
	// Entries são os upgrades a executar, na ordem (respeitando MaxPerCycle).
	Entries []PlanEntry
	// Skips listam os itens avaliados e pulados, com motivo.
	Skips []PlanSkip
	// MaxPerCycle é o limite vigente usado no plano.
	MaxPerCycle int
}

// normalizeUpdateSource espelha normalizeUpdateSource do frontend
// (frontend/js/app-store-updates.js): choco/chocolatey → "chocolatey",
// winget → "winget", vazio/unknown → "winget" (default conservador).
func normalizeUpdateSource(source string) string {
	normalized := strings.ToLower(strings.TrimSpace(source))
	switch normalized {
	case "choco", "chocolatey":
		return "chocolatey"
	case "winget":
		return "winget"
	}
	if normalized == "" {
		return "winget"
	}
	return normalized
}

// buildUpdateUpgradeTarget monta o target "source::id" a partir do update
// pendente (idêntico a buildUpdateUpgradeTarget do frontend).
func buildUpdateUpgradeTarget(item models.UpgradeItem) string {
	id := strings.TrimSpace(item.ID)
	if id == "" {
		return ""
	}
	return normalizeUpdateSource(item.Source) + TargetSeparator + id
}

// matchKey é a chave de cruzamento política × pendentes: installationType e
// packageId normalizados (lowercase, choco/chocolatey unificados) — match
// case-insensitive.
func matchKey(installationType, packageID string) string {
	t := strings.ToLower(strings.TrimSpace(installationType))
	switch t {
	case "choco", "chocolatey":
		t = "chocolatey"
	case "winget":
		t = "winget"
	}
	return t + "|" + strings.ToLower(strings.TrimSpace(packageID))
}

// ShouldRunNow informa se o auto-update pode executar AGORA contra a janela de
// manutenção. Semântica igual ao deferral de power:
//   - janela DESLIGADA (Enabled=false) → true (sem gate);
//   - janela ATIVA e now dentro → true;
//   - janela ATIVA e now fora → false (aguardar a próxima janela).
func ShouldRunNow(win maintenance.Window, now time.Time) bool {
	if !win.Enabled {
		return true
	}
	return win.Contains(now)
}

// Plan é o planner puro (testável sem I/O): cruza a política efetiva da Loja
// com os updates pendentes e devolve o plano do ciclo.
//
// Ordem de avaliação por item aprovado com AutoUpdateEnabled=true:
//  1. no_update — não está na lista de pendentes;
//  2. automated_by_task — isAutomated(installationType, packageID) true;
//  3. outside_window — janela ativa e now fora dela;
//  4. senão vira entry (target "source::id"), até MaxPerCycle.
//
// Pendentes cujo (source,id) NÃO correspondem a nenhum item da política geram
// skip "denied" (defensivo: teoricamente impossível, pois o Upgrade passaria
// pelo gate da Loja).
func Plan(policy appstore.EffectivePolicy, pending []models.UpgradeItem, isAutomated func(installationType, packageID string) bool, now time.Time) UpdatePlan {
	return planWithLimit(policy, pending, isAutomated, now, maintenance.Window{}, MaxPerCycle)
}

// planWithLimit é a implementação interna com janela e limite explícitos
// (usada pelos testes e por RunOnce).
func planWithLimit(policy appstore.EffectivePolicy, pending []models.UpgradeItem, isAutomated func(installationType, packageID string) bool, now time.Time, window maintenance.Window, maxPerCycle int) UpdatePlan {
	if maxPerCycle <= 0 {
		maxPerCycle = MaxPerCycle
	}

	pendingByKey := make(map[string]models.UpgradeItem, len(pending))
	for _, item := range pending {
		key := matchKey(normalizeUpdateSource(item.Source), item.ID)
		if strings.TrimSpace(item.ID) == "" {
			continue
		}
		if _, seen := pendingByKey[key]; !seen {
			pendingByKey[key] = item
		}
	}

	// Pendentes órfãos (nenhum item na política): skip denied, defensivo.
	approvedKeys := make(map[string]struct{}, len(policy.Items))
	for _, item := range policy.Items {
		if strings.TrimSpace(item.PackageID) == "" {
			continue
		}
		key := matchKey(item.InstallationType, item.PackageID)
		approvedKeys[key] = struct{}{}
	}
	plan := UpdatePlan{MaxPerCycle: maxPerCycle}
	for _, item := range pending {
		key := matchKey(normalizeUpdateSource(item.Source), item.ID)
		if _, ok := approvedKeys[key]; ok {
			continue
		}
		plan.Skips = append(plan.Skips, PlanSkip{
			InstallationType: normalizeUpdateSource(item.Source),
			PackageID:        strings.TrimSpace(item.ID),
			Reason:           SkipReasonDenied,
		})
	}

	windowBlocked := !ShouldRunNow(window, now)
	plan.WindowBlocked = windowBlocked
	plan.MaxPerCycle = maxPerCycle

	for _, item := range policy.Items {
		if strings.TrimSpace(item.PackageID) == "" {
			continue
		}
		// Aprovação + flag de auto-update: sem a flag, o item continua
		// instalável manualmente mas não entra no auto-update.
		if !item.AutoUpdateEnabled {
			continue
		}
		key := matchKey(item.InstallationType, item.PackageID)
		pendingItem, inPending := pendingByKey[key]
		if !inPending {
			plan.Skips = append(plan.Skips, PlanSkip{InstallationType: item.InstallationType, PackageID: item.PackageID, Reason: SkipReasonNoUpdate})
			continue
		}
		if isAutomated != nil && isAutomated(item.InstallationType, item.PackageID) {
			plan.Skips = append(plan.Skips, PlanSkip{InstallationType: item.InstallationType, PackageID: item.PackageID, Reason: SkipReasonAutomatedByTask})
			continue
		}
		if windowBlocked {
			plan.Skips = append(plan.Skips, PlanSkip{InstallationType: item.InstallationType, PackageID: item.PackageID, Reason: SkipReasonOutsideWindow})
			continue
		}
		if len(plan.Entries) >= maxPerCycle {
			// Acima do limite do ciclo: o item fica fora do plano e é
			// reavaliado no próximo ciclo (não é um skip de motivo).
			continue
		}
		target := buildUpdateUpgradeTarget(pendingItem)
		if target == "" {
			plan.Skips = append(plan.Skips, PlanSkip{InstallationType: item.InstallationType, PackageID: item.PackageID, Reason: SkipReasonNoUpdate})
			continue
		}
		plan.Entries = append(plan.Entries, PlanEntry{
			Target:           target,
			InstallationType: item.InstallationType,
			PackageID:        item.PackageID,
			Source:           normalizeUpdateSource(pendingItem.Source),
			AvailableVersion: pendingItem.AvailableVersion,
		})
	}
	return plan
}

// Deps são as dependências injetadas pelo App (app.go).
type Deps struct {
	// LoadPolicy carrega a política efetiva da Loja (AppStoreSvc.LoadEffectivePolicy).
	LoadPolicy func(ctx context.Context) (appstore.EffectivePolicy, error)
	// PendingUpdates varre winget/choco (UpdatesSvc.GetPendingUpdates). Uma
	// varredura por ciclo.
	PendingUpdates func() ([]models.UpgradeItem, error)
	// Upgrade executa o upgrade de um target "source::id" (InventorySvc.Upgrade)
	// — passa pelo gate da Loja e pela fila única de pacotes.
	Upgrade func(ctx context.Context, target string) (string, error)
	// IsAutomated informa se a AUTOMAÇÃO é dona do pacote (precedência:
	// automação sobrepõe a Loja) — App → AutomationSvc.PackageAuthorizedByPolicy.
	IsAutomated func(installationType, packageID string) bool
	// Window devolve a janela de manutenção efetiva da configuração do agente.
	Window func() maintenance.Window
	// Logf registra uma linha no log do agente (a.Logs).
	Logf func(string)
	// EmitAutoUpdated é o evento best-effort para a UI ("store:auto-updated").
	EmitAutoUpdated func(count int)
	// RecordExecution grava UMA execução no ledger local de auto-update (F3).
	// Best-effort: nil ou falha de gravação NÃO interrompe o ciclo. O App
	// persiste no SQLite (app_auto_update_executions) e expõe via IPC/evento.
	RecordExecution func(execution AutoUpdateExecution)
	// Now permite testar o relógio. Nil = time.Now.
	Now func() time.Time
	// MaxPerCycle override (0 = MaxPerCycle default).
	MaxPerCycle int
	// MinSpacing override (0 = MinSpacing default; negativo desativa).
	MinSpacing time.Duration
	// RunInterval/RunJitterMax/StartDelay overrides (0 = default).
	RunInterval  time.Duration
	RunJitterMax time.Duration
	StartDelay   time.Duration
}

// Service orquestra os ciclos de auto-update da Loja.
type Service struct {
	deps Deps
	now  func() time.Time
}

// New cria o service de auto-update.
func New(deps Deps) *Service {
	return &Service{deps: deps, now: time.Now}
}

// Resultados canônicos do ledger local de auto-update (F3).
const (
	AutoUpdateResultSuccess = "success"
	AutoUpdateResultError   = "error"
)

func (s *Service) logf(line string) {
	if s.deps.Logf != nil {
		s.deps.Logf("[autoupdate pacote] " + line)
	}
}

// recordExecution grava UMA execução no ledger local (F3) — best-effort:
// nil ou erro/pânico do persistidor nunca interrompem o ciclo (log e segue).
func (s *Service) recordExecution(execution AutoUpdateExecution) {
	if s == nil || s.deps.RecordExecution == nil {
		return
	}
	defer func() { _ = recover() }() // defesa: persistidor não derruba o ciclo
	s.deps.RecordExecution(execution)
	if execution.Result == AutoUpdateResultError {
		s.logf(fmt.Sprintf("ledger: %s::%s resultado=%s erro=%s", execution.Source, execution.PackageID, execution.Result, execution.ErrorMessage))
		return
	}
	s.logf(fmt.Sprintf("ledger: %s::%s resultado=%s versão alvo=%s", execution.Source, execution.PackageID, execution.Result, execution.TargetVersion))
}

func (s *Service) nowTime() time.Time {
	if s.deps.Now != nil {
		return s.deps.Now()
	}
	return time.Now()
}

func (s *Service) maxPerCycle() int {
	if s.deps.MaxPerCycle > 0 {
		return s.deps.MaxPerCycle
	}
	return MaxPerCycle
}

func (s *Service) minSpacing() time.Duration {
	if s.deps.MinSpacing < 0 {
		return 0
	}
	if s.deps.MinSpacing == 0 {
		return MinSpacing
	}
	return s.deps.MinSpacing
}

// RunOnce executa UM ciclo: carrega a política, varre os pendentes uma única
// vez, planeja e executa (respeitando MaxPerCycle e o espaçamento mínimo).
// Nunca reinicia o agente; erro de política/varredura encerra o ciclo com log.
func (s *Service) RunOnce(ctx context.Context) error {
	if s == nil {
		return nil
	}
	now := s.nowTime()
	window := maintenance.Window{}
	if s.deps.Window != nil {
		window = s.deps.Window()
	}
	if !ShouldRunNow(window, now) {
		s.logf(fmt.Sprintf("fora da janela de manutenção (enabled=true, dias=%v, duração=%dmin) — ciclo adiado", window.Days, window.DurationMinutes))
		return nil
	}

	if s.deps.LoadPolicy == nil || s.deps.PendingUpdates == nil || s.deps.Upgrade == nil {
		return errors.New("autoupdate: dependências incompletas (LoadPolicy/PendingUpdates/Upgrade)")
	}

	policy, err := s.deps.LoadPolicy(ctx)
	if err != nil {
		return fmt.Errorf("política efetiva da loja: %w", err)
	}
	pending, err := s.deps.PendingUpdates()
	if err != nil {
		return fmt.Errorf("varredura de updates pendentes: %w", err)
	}

	var isAutomated func(installationType, packageID string) bool
	if s.deps.IsAutomated != nil {
		isAutomated = s.deps.IsAutomated
	}
	plan := planWithLimit(policy, pending, isAutomated, now, window, s.maxPerCycle())

	for _, skip := range plan.Skips {
		s.logf(fmt.Sprintf("skip %s::%s motivo=%s", normalizeUpdateSource(skip.InstallationType), skip.PackageID, skip.Reason))
	}
	s.logf(fmt.Sprintf("plano: %d upgrade(s) neste ciclo (limite %d), %d skip(s)", len(plan.Entries), plan.MaxPerCycle, len(plan.Skips)))

	executed := 0
	spacing := s.minSpacing()
	for i, entry := range plan.Entries {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("ciclo cancelado após %d upgrade(s): %w", executed, err)
			}
		}
		if i > 0 && spacing > 0 {
			// Espaçamento mínimo entre upgrades do mesmo ciclo.
			select {
			case <-ctx.Done():
				return fmt.Errorf("ciclo cancelado após %d upgrade(s): %w", executed, ctx.Err())
			case <-time.After(spacing):
			}
		}
		out, err := s.deps.Upgrade(ctx, entry.Target)
		if err != nil {
			s.logf(fmt.Sprintf("upgrade %s falhou: %v", entry.Target, err))
			s.recordExecution(AutoUpdateExecution{
				InstallationType: entry.InstallationType,
				PackageID:        entry.PackageID,
				Source:           entry.Source,
				Target:           entry.Target,
				TargetVersion:    entry.AvailableVersion,
				Result:           AutoUpdateResultError,
				ErrorMessage:     err.Error(),
				ExecutedAt:       s.nowTime(),
			})
			continue
		}
		executed++
		s.logf(fmt.Sprintf("upgrade %s ok (versão alvo %s)", entry.Target, entry.AvailableVersion))
		s.recordExecution(AutoUpdateExecution{
			InstallationType: entry.InstallationType,
			PackageID:        entry.PackageID,
			Source:           entry.Source,
			Target:           entry.Target,
			TargetVersion:    entry.AvailableVersion,
			Result:           AutoUpdateResultSuccess,
			ExecutedAt:       s.nowTime(),
		})
		if out != "" {
			s.logf(strings.TrimSpace(out))
		}
	}

	if executed > 0 && s.deps.EmitAutoUpdated != nil {
		// Best-effort: sem listener obrigatório na UI.
		s.deps.EmitAutoUpdated(executed)
	}
	return nil
}

// Run roda o loop do auto-update (deve rodar SÓ em ServiceMode — na UI
// companion quem executa é o serviço): um RunOnce após StartDelay de warm-up e
// ciclos a cada RunInterval com jitter aleatório ≤ RunJitterMax. Encerra no
// cancelamento do ctx (shutdown do app).
func (s *Service) Run(ctx context.Context) {
	if s == nil || ctx == nil {
		return
	}
	startDelay := s.deps.StartDelay
	if startDelay <= 0 {
		startDelay = StartDelay
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(startDelay):
	}

	if err := s.RunOnce(ctx); err != nil && ctx.Err() == nil {
		s.logf("ciclo inicial falhou: " + err.Error())
	}

	interval := s.deps.RunInterval
	if interval <= 0 {
		interval = RunInterval
	}
	jitterMax := s.deps.RunJitterMax
	if jitterMax <= 0 {
		jitterMax = RunJitterMax
	}
	for {
		delay := interval + time.Duration(rand.Int64N(int64(jitterMax)+1))
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if err := s.RunOnce(ctx); err != nil && ctx.Err() == nil {
			s.logf("ciclo falhou: " + err.Error())
		}
	}
}
