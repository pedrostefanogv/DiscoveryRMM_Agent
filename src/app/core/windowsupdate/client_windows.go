//go:build windows

package windowsupdate

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	ole "github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
)

// HRESULTs relevantes ao COM/WUA.
const (
	hresultSFalse             uintptr = 0x00000001 // COM ja inicializado neste thread
	hresultRPC_E_CHANGED_MODE uintptr = 0x80010106 // thread ja inicializado em outro apartment
	// HRESULT de criterio invalido do IUpdateSearcher (BrowseOnly em versoes
	// antigas). O scan refaz a busca sem o criterio.
	hresultInvalidCriteria uintptr = 0x80240032
)

// Criterios canonicos do WUA. "Type='Software'" inclui atualizacoes
// opcionais e upgrades (BrowseOnly nao e filtrado) e evita ler drivers.
const (
	wuaPendingSoftwareCriteria   = "IsInstalled=0 and Type='Software'"
	wuaInstalledSoftwareCriteria = "IsInstalled=1 and Type='Software'"
)

// Codigos de resultado de download/instalacao do WUA.
const (
	wuaResultNotStarted       = 0
	wuaResultInProgress       = 1
	wuaResultSucceeded        = 2
	wuaResultSucceededWithErr = 3
	wuaResultFailed           = 4
	wuaResultAborted          = 5
)

// wuaClient implementa Client sobre Microsoft.Update.Session (COM late binding).
// Uma unica operacao WUA roda por vez: scan, install e hide sao serializados
// pelo flag opBusy. Timeouts nao cancelam o COM (nao ha como), apenas devolvem
// o controle ao chamador; a operacao orfa termina e libera o flag.
type wuaClient struct {
	opts   Options
	opBusy atomic.Bool
}

func newPlatformClient(opts Options) Client {
	return &wuaClient{opts: opts}
}

// withOp serializa e limita uma operacao COM. O thread e travado e o COM
// inicializado como MTA dentro da goroutine (apartment e thread-affine).
func withOp[T any](c *wuaClient, ctx context.Context, timeout time.Duration, fn func() (T, error)) (T, error) {
	var zero T
	if ctx == nil {
		ctx = context.Background()
	}
	if !c.opBusy.CompareAndSwap(false, true) {
		return zero, ErrBusy
	}

	type opResult struct {
		value T
		err   error
	}
	ch := make(chan opResult, 1)
	go func() {
		defer c.opBusy.Store(false)
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		needsUninit, err := comInitializeMTA()
		if err != nil {
			ch <- opResult{err: ErrWuaUnavailable}
			return
		}
		if needsUninit {
			defer ole.CoUninitialize()
		}
		v, err := fn()
		ch <- opResult{value: v, err: err}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case r := <-ch:
		return r.value, r.err
	case <-timer.C:
		return zero, ErrTimeout
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}

// comInitializeMTA inicializa o COM como MTA no thread atual.
// RPC_E_CHANGED_MODE nao e erro fatal (thread ja inicializado em outro
// apartment); nesse caso nao desinicializamos.
func comInitializeMTA() (needsUninit bool, err error) {
	err = ole.CoInitializeEx(0, ole.COINIT_MULTITHREADED)
	if err == nil {
		return true, nil
	}
	var oleErr *ole.OleError
	if errors.As(err, &oleErr) {
		switch oleErr.Code() {
		case hresultSFalse:
			return true, nil
		case hresultRPC_E_CHANGED_MODE:
			return false, nil
		}
	}
	return false, err
}

func (c *wuaClient) Probe(ctx context.Context) ProbeInfo {
	info := ProbeInfo{Supported: true}
	if state := windowsUpdateServiceState(); state != "" {
		info.ServiceState = state
	}
	if wsus := wsusServerFromPolicy(); wsus != "" {
		info.WSUSServer = wsus
		info.PolicyManaged = true
	}
	// Cria o objeto COM para provar que o WUA existe. Nao roda busca.
	_, err := withOp(c, ctx, c.opts.ShortTimeout, func() (bool, error) {
		unknown, err := oleutil.CreateObject("Microsoft.Update.Session")
		if err != nil {
			return false, ErrWuaUnavailable
		}
		defer unknown.Release()
		disp, err := unknown.QueryInterface(ole.IID_IDispatch)
		if err != nil {
			return false, ErrWuaUnavailable
		}
		disp.Release()
		return true, nil
	})
	if err != nil {
		info.Supported = false
		switch {
		case errors.Is(err, ErrWuaUnavailable):
			info.ErrorCode = ScanErrorWuaUnavailable
		case errors.Is(err, ErrTimeout):
			info.ErrorCode = ScanErrorTimeout
		default:
			info.ErrorCode = ScanErrorFailed
		}
	}
	if strings.EqualFold(info.ServiceState, "disabled") {
		info.ErrorCode = ScanErrorServiceDisabled
	}
	return info
}

func (c *wuaClient) Scan(ctx context.Context, opts ScanOptions) (*ScanResult, error) {
	// Servico desabilitado: sem WUA, mas a UI precisa de explicacao. Devolve um
	// resultado valido (parcial) com a sonda de estado e o erro tipado — o
	// chamador sobe o envelope com scan.status=error/errorCode.
	if strings.EqualFold(windowsUpdateServiceState(), "disabled") {
		return unavailableScanResult(), ErrServiceDisabled
	}
	// Invalida o cache do CBS ANTES do scan: scanWUA compoe o estado (state) com
	// a contagem de KBs instaladas lida no fim, e o cache podia estar velho.
	// Invalidar depois deixava o PROPRIO envelope do scan com contagem antiga
	// (a baseline era recalculada ja fresca, e nenhum ciclo detectava a mudanca).
	InvalidateInstalledKBsCache()
	result, err := withOp(c, ctx, c.opts.ScanTimeout, func() (*ScanResult, error) {
		return scanWUA(opts)
	})
	if err != nil && result == nil {
		partial := unavailableScanResult()
		switch {
		case errors.Is(err, ErrWuaUnavailable):
			partial.State.PolicyErrorCode = ScanErrorWuaUnavailable
		case errors.Is(err, ErrTimeout):
			partial.State.PolicyErrorCode = ScanErrorTimeout
		default:
			partial.State.PolicyErrorCode = ScanErrorFailed
		}
		return partial, err
	}
	// Scan WUA concluido com sucesso: o conjunto de KBs instaladas pode ter
	// mudado, entao a proxima sonda barata deve reler o CBS (nao usar o cache).
	if result != nil && !result.Partial {
		InvalidateInstalledKBsCache()
	}
	return result, err
}

// ProbeState devolve a sonda barata do estado local (sem WUA/COM).
func (c *wuaClient) ProbeState(ctx context.Context) WindowsUpdateState {
	state := ProbeLocalState()
	if ctx != nil && ctx.Err() != nil {
		state.PolicyErrorCode = ScanErrorTimeout
	}
	return state
}

// unavailableScanResult monta o ScanResult de uma maquina onde o WUA nao pode
// rodar. Partial=true impede o servidor de interpretar a lista vazia como
// remocao do inventario.
func unavailableScanResult() *ScanResult {
	now := time.Now().UTC()
	state := ProbeLocalState()
	reboot := RebootInfo{Pending: state.RebootPending, Reasons: state.RebootReasons, CheckedAt: now}
	return &ScanResult{
		Updates:       []Update{},
		Partial:       true,
		PendingReboot: reboot,
		State:         state,
		StartedAt:     now,
		DurationMs:    0,
		Source:        SourceWUA,
	}
}

func (c *wuaClient) Install(ctx context.Context, targets []InstallTarget) (*InstallResult, error) {
	if len(targets) == 0 {
		return &InstallResult{}, nil
	}
	if len(targets) > MaxUpdatesPerCommand {
		return nil, fmt.Errorf("lote de %d updates excede o limite de %d", len(targets), MaxUpdatesPerCommand)
	}
	if strings.EqualFold(windowsUpdateServiceState(), "disabled") {
		return nil, ErrServiceDisabled
	}
	return withOp(c, ctx, c.opts.InstallTimeout, func() (*InstallResult, error) {
		return installWUA(targets)
	})
}

func (c *wuaClient) Hide(ctx context.Context, updateKeys []string, hidden bool) (int, error) {
	if len(updateKeys) == 0 {
		return 0, nil
	}
	return withOp(c, ctx, c.opts.ShortTimeout, func() (int, error) {
		return hideWUA(updateKeys, hidden)
	})
}

// MaxUpdatesPerCommand limita o lote aceito em um comando (defesa contra
// payload abusivo).
const MaxUpdatesPerCommand = 100

// ---- internals -------------------------------------------------------------

type wuaSession struct {
	session *ole.IDispatch
}

func openSession() (*wuaSession, error) {
	unknown, err := oleutil.CreateObject("Microsoft.Update.Session")
	if err != nil {
		return nil, ErrWuaUnavailable
	}
	disp, err := unknown.QueryInterface(ole.IID_IDispatch)
	unknown.Release()
	if err != nil {
		return nil, ErrWuaUnavailable
	}
	return &wuaSession{session: disp}, nil
}

func (s *wuaSession) Close() {
	if s != nil && s.session != nil {
		s.session.Release()
		s.session = nil
	}
}

func (s *wuaSession) searcher() (*ole.IDispatch, error) {
	raw, err := oleutil.CallMethod(s.session, "CreateUpdateSearcher")
	if err != nil {
		return nil, err
	}
	disp := raw.ToIDispatch()
	// Ownership transferido: quem chama faz Release. NÃO chamar raw.Clear()
	// (o VariantClear liberaria a mesma referência de novo).
	if disp == nil {
		raw.Clear()
		return nil, ErrWuaUnavailable
	}
	return disp, nil
}

// searchResult devolve o IDispatch de ISearchResult.Updates (colecao).
func searchUpdatesCollection(searcher *ole.IDispatch, criteria string) (*ole.IDispatch, error) {
	raw, err := oleutil.CallMethod(searcher, "Search", criteria)
	if err != nil {
		return nil, err
	}
	result := raw.ToIDispatch()
	if result == nil {
		raw.Clear()
		return nil, ErrWuaUnavailable
	}
	defer result.Release()

	updatesRaw, err := oleutil.GetProperty(result, "Updates")
	if err != nil {
		return nil, err
	}
	collection := updatesRaw.ToIDispatch()
	if collection == nil {
		updatesRaw.Clear()
		return nil, ErrWuaUnavailable
	}
	// A coleção é devolvida ao chamador, que faz Release.
	return collection, nil
}

// pendingCriteria devolve os criterios da busca de pendentes.
//
// A diferenca em relacao ao filtro antigo e NAO usar BrowseOnly=0:
// BrowseOnly=1 = atualizacao OPCIONAL (inclusive upgrades de versao como
// "Windows 11, version 26H2"). O Windows Update mostra esses pacotes como
// disponiveis, entao o inventario do RMM tambem precisa mostra-los; o filtro
// escondia o upgrade e a maquina aparecia "atualizada" no RMM.
//
// Type='Software' e mantido de proposito: e o que evita ler todos os drivers
// (Type='Driver') a cada scan. Feature updates/upgrades continuam sendo
// Software; drivers seguem fora do escopo, e includeDrivers=true remove o
// filtro para quem pedir explicitamente. As variantes sem Type/IsHidden sao
// fallback para WUA que rejeite o criterio (HRESULT de criterio invalido).
func pendingCriteria(includeHidden, includeDrivers bool) []string {
	base := wuaPendingSoftwareCriteria
	if includeDrivers {
		base = "IsInstalled=0"
	}
	if includeHidden {
		return []string{base, "IsInstalled=0"}
	}
	return []string{base + " and IsHidden=0", "IsInstalled=0 and IsHidden=0", "IsInstalled=0"}
}

// searchPending executa a busca de pendentes tentando variantes de criterio,
// caindo para a variante sem IsHidden quando o WUA rejeita o criterio.
//
// Importante (deteccao de pendentes): avalia TODAS as variantes e escolhe a
// MELHOR — mais itens incluidos e menos itens "unreadable". Uma variante de
// criterio pode listar o pendente de forma que Item(i)/leitura falhe no
// contexto do servico (ex.: "Windows 11 Insider Preview Quality Update" ficava
// unreadable na variante com IsHidden=0), enquanto outra variante le o mesmo
// item sem erro. Antes a primeira variante que respondia vencia, mesmo com
// descartes, e o pendente real nunca chegava ao inventario.
func searchPending(searcher *ole.IDispatch, includeHidden, includeDrivers bool) ([]Update, ScanCounters, error) {
	var lastErr error
	var bestUpdates []Update
	var bestCounters ScanCounters
	for _, criteria := range pendingCriteria(includeHidden, includeDrivers) {
		collection, err := searchUpdatesCollection(searcher, criteria)
		if err != nil {
			lastErr = err
			var oleErr *ole.OleError
			if errors.As(err, &oleErr) && oleErr.Code() == hresultInvalidCriteria {
				continue
			}
			// Erro não-critico da variante: tenta a proxima (o scan do WUA
			// falha por criterio específico da maquina; outra variante pode
			// responder).
			continue
		}
		updates, counters, readErr := readUpdates(collection, includeDrivers)
		collection.Release()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if bestUpdates == nil || betterScanResult(counters, bestCounters) {
			bestUpdates, bestCounters = updates, counters
		}
	}
	if bestUpdates != nil {
		return bestUpdates, bestCounters, nil
	}
	return nil, ScanCounters{}, lastErr
}

// betterScanResult compara dois resultados de busca por variante de criterio:
// prefere mais itens incluidos; em empate, menos unreadable; em novo empate,
// mais leituras. O desempate final é estável (primeira variante vence).
func betterScanResult(a, b ScanCounters) bool {
	if a.Included != b.Included {
		return a.Included > b.Included
	}
	aUnread := a.Skipped[SkipReasonUnreadable]
	bUnread := b.Skipped[SkipReasonUnreadable]
	if aUnread != bUnread {
		return aUnread < bUnread
	}
	return a.Read > b.Read
}

// ScanCounters contabiliza o que ficou fora do inventario, por motivo. O
// objetivo e NUNCA descartar em silencio: "Windows 11 Insider Preview Quality
// Update" (categoria Upgrades) era descartado e o servico REAL instalado nesta
// frota sumia da aba de atualizacoes.
type ScanCounters struct {
	Read     int
	Included int
	Skipped  map[string]int
	SkippedN int
	Upgrades int
	Drivers  int
	// UnreadableErr guarda o erro COM do ultimo item ilegivel (diagnóstico do
	// porquê um pendente real pode ter ficado fora do inventario).
	UnreadableErr string
}

func (c *ScanCounters) addSkipped(reason string) {
	if c.Skipped == nil {
		c.Skipped = make(map[string]int, 4)
	}
	c.Skipped[reason]++
	c.SkippedN++
}

// collectionItem le o item i de uma colecao WUA. O Item de IUpdateCollection
// e exposto como propget em algumas versoes do WUA: invocacao por DISPATCH_METHOD
// (CallMethod) falha com DISP_E_MEMBERNOTFOUND ("Membro nao encontrado") e o
// pendente REAL era descartado como unreadable — o update nunca chegava ao
// inventario (reproduzido localmente com "Windows 11 Insider Preview Quality
// Update"). Tentativa 1: metodo; fallback: propget (funciona nas duas formas).
func collectionItem(coll *ole.IDispatch, i int) (*ole.VARIANT, error) {
	itemRaw, err := oleutil.CallMethod(coll, "Item", i)
	if err == nil {
		return itemRaw, nil
	}
	itemRaw, propErr := oleutil.GetProperty(coll, "Item", i)
	if propErr == nil {
		return itemRaw, nil
	}
	return nil, err // erro original (diagnostico)
}

func readUpdates(collection *ole.IDispatch, includeDrivers bool) ([]Update, ScanCounters, error) {
	counters := ScanCounters{}
	countRaw, err := oleutil.GetProperty(collection, "Count")
	if err != nil {
		return nil, counters, err
	}
	count := variantInt(countRaw)
	countRaw.Clear()
	if count < 0 {
		count = 0
	}
	out := make([]Update, 0, count)
	for i := 0; i < count; i++ {
		itemRaw, err := collectionItem(collection, i)
		if err != nil {
			counters.Read++
			counters.UnreadableErr = err.Error()
			counters.addSkipped(SkipReasonUnreadable)
			continue
		}
		item := itemRaw.ToIDispatch()
		if item == nil {
			itemRaw.Clear()
			counters.Read++
			counters.addSkipped(SkipReasonUnreadable)
			continue
		}
		counters.Read++
		update, skip := readUpdate(item)
		item.Release()
		if skip {
			counters.addSkipped(SkipReasonNoIdentity)
			continue
		}
		// Upgrades (feature updates / "Windows 11 Insider Preview Quality
		// Update") SAO inventariados: e exatamente o tipo de pacote que o
		// Windows Update baixa e instala nesta frota. Somente drivers ficam de
		// fora, e apenas quando o chamador pediu (IncludeDrivers=false), com
		// contabilizacao explicita.
		if isUpgradeCategory(update) {
			counters.Upgrades++
		}
		if !includeDrivers && isDriverUpdate(update) {
			counters.Drivers++
			counters.addSkipped(SkipReasonDriver)
			continue
		}
		counters.Included++
		out = append(out, update)
	}
	return out, counters, nil
}

func readUpdate(update *ole.IDispatch) (Update, bool) {
	u := Update{Source: SourceWUA}
	title, description, severity, supportURL := "", "", "", ""
	var isDownloaded, isMandatory, isHidden, rebootRequired bool
	var maxDownloadSize int64
	var lastDeployed *time.Time
	var updateID string
	var revision int
	var kbIDs []string
	var categories []string

	if raw, err := oleutil.GetProperty(update, "Title"); err == nil {
		title = variantString(raw)
		raw.Clear()
	}
	if raw, err := oleutil.GetProperty(update, "Description"); err == nil {
		description = variantString(raw)
		raw.Clear()
	}
	if raw, err := oleutil.GetProperty(update, "MsrcSeverity"); err == nil {
		severity = variantString(raw)
		raw.Clear()
	}
	if raw, err := oleutil.GetProperty(update, "SupportUrl"); err == nil {
		supportURL = variantString(raw)
		raw.Clear()
	}
	if raw, err := oleutil.GetProperty(update, "IsDownloaded"); err == nil {
		isDownloaded = variantBool(raw)
		raw.Clear()
	}
	if raw, err := oleutil.GetProperty(update, "IsMandatory"); err == nil {
		isMandatory = variantBool(raw)
		raw.Clear()
	}
	if raw, err := oleutil.GetProperty(update, "IsHidden"); err == nil {
		isHidden = variantBool(raw)
		raw.Clear()
	}
	if raw, err := oleutil.GetProperty(update, "RebootRequired"); err == nil {
		rebootRequired = variantBool(raw)
		raw.Clear()
	}
	// MaxDownloadSize (IUpdate, bytes): VT_I4/VT_UI4/VT_I8 via late binding. E
	// um dado OPCIONAL: se a leitura falhar (WUA antigo, propriedade ausente) o
	// update continua valido com 0 (omitido no JSON).
	if raw, err := oleutil.GetProperty(update, "MaxDownloadSize"); err == nil {
		maxDownloadSize = variantInt64(raw)
		raw.Clear()
	}
	if raw, err := oleutil.GetProperty(update, "LastDeploymentChangeTime"); err == nil {
		lastDeployed = variantTime(raw)
		raw.Clear()
	}
	if raw, err := oleutil.GetProperty(update, "Identity"); err == nil {
		if identity := raw.ToIDispatch(); identity != nil {
			if idRaw, err := oleutil.GetProperty(identity, "UpdateID"); err == nil {
				updateID = variantString(idRaw)
				idRaw.Clear()
			}
			if revRaw, err := oleutil.GetProperty(identity, "RevisionNumber"); err == nil {
				revision = variantInt(revRaw)
				revRaw.Clear()
			}
			// identity.Release() libera a referência do VARIANT: não chamar
			// raw.Clear() depois (VariantClear liberaria de novo).
			identity.Release()
		} else {
			raw.Clear()
		}
	}
	if raw, err := oleutil.GetProperty(update, "KBArticleIDs"); err == nil {
		if coll := raw.ToIDispatch(); coll != nil {
			kbIDs = readStringCollection(coll)
			coll.Release()
		} else {
			raw.Clear()
		}
	}
	var categoryIDs []string
	if raw, err := oleutil.GetProperty(update, "Categories"); err == nil {
		if coll := raw.ToIDispatch(); coll != nil {
			categories, categoryIDs = readCategories(coll)
			coll.Release()
		} else {
			raw.Clear()
		}
	}

	kbIDs = NormalizeKBIDs(kbIDs)
	updateKey := UpdateKeyFor(updateID, revision, kbIDs)
	if updateKey == "" {
		return Update{}, true
	}

	u.UpdateKey = updateKey
	u.UpdateID = NormalizeGUID(updateID)
	u.Revision = revision
	u.KBIDs = kbIDs
	if len(kbIDs) > 0 {
		u.KBID = kbIDs[0]
	}
	u.Title = truncate(title, 500)
	u.Description = truncate(description, 2000)
	// Severidade com proveniencia (WU-02/D10): MsrcSeverity vence; sem rating do
	// fornecedor o agente deriva com teto "Important" (nunca "Critical").
	u.Severity, u.SeveritySource = ResolveSeverity(severity, categories, title)
	u.SupportURL = truncate(supportURL, 1000)
	u.IsPending = true
	u.IsDownloaded = isDownloaded
	u.IsMandatory = isMandatory
	u.IsHidden = isHidden
	u.RequiresReboot = rebootRequired
	if maxDownloadSize > 0 {
		u.MaxDownloadSize = maxDownloadSize
	}
	u.PublishedAt = lastDeployed
	u.LastDeployedAt = lastDeployed
	u.Categories = categories
	u.CategoryIDs = categoryIDs
	// WU-14: classifica por GUID (estavel por idioma) e cai para o nome quando
	// o WUA nao expoe CategoryID (agente/WUA antigo).
	u.Classification = ClassifyCategory(categoryIDs, categories)
	u.UpdateType = DeriveUpdateType(categoryIDs, categories)
	return u, false
}

// isDriverUpdate informa se o update e um driver (fora do escopo do modulo).
func isDriverUpdate(u Update) bool {
	if IsDriverCategory(u.CategoryIDs, u.Categories) {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(u.Classification), "drivers")
}

// isUpgradeCategory informa se o update pertence a categoria Upgrades. Usado
// APENAS para diagnostico/metrica: nao descarta nada.
func isUpgradeCategory(u Update) bool {
	return IsUpgradeCategory(u.CategoryIDs, u.Categories)
}

// classify escolhe a classificacao exibida a partir das categorias do WUA.
func classify(categories []string) string {
	priority := []string{
		"Critical Updates",
		"Security Updates",
		"Definition Updates",
		"Update Rollups",
		"Service Packs",
		"Feature Packs",
		"Updates",
		"Tools",
	}
	lower := make(map[string]string, len(categories))
	for _, category := range categories {
		lower[strings.ToLower(strings.TrimSpace(category))] = category
	}
	for _, wanted := range priority {
		if original, ok := lower[strings.ToLower(wanted)]; ok {
			return original
		}
	}
	if len(categories) > 0 {
		return categories[0]
	}
	return ""
}

func readStringCollection(coll *ole.IDispatch) []string {
	countRaw, err := oleutil.GetProperty(coll, "Count")
	if err != nil {
		return nil
	}
	count := variantInt(countRaw)
	countRaw.Clear()
	out := make([]string, 0, count)
	for i := 0; i < count; i++ {
		itemRaw, err := collectionItem(coll, i)
		if err != nil {
			continue
		}
		out = append(out, variantString(itemRaw))
		itemRaw.Clear()
	}
	return out
}

// readCategories devolve nomes e CategoryIDs das categorias do update. Os dois
// sao usados: nome para exibicao, GUID para classificacao independente de
// idioma (WU-14).
func readCategories(coll *ole.IDispatch) (names []string, ids []string) {
	countRaw, err := oleutil.GetProperty(coll, "Count")
	if err != nil {
		return nil, nil
	}
	count := variantInt(countRaw)
	countRaw.Clear()
	names = make([]string, 0, count)
	ids = make([]string, 0, count)
	for i := 0; i < count; i++ {
		itemRaw, err := collectionItem(coll, i)
		if err != nil {
			continue
		}
		category := itemRaw.ToIDispatch()
		if category == nil {
			itemRaw.Clear()
			continue
		}
		if nameRaw, err := oleutil.GetProperty(category, "Name"); err == nil {
			name := variantString(nameRaw)
			nameRaw.Clear()
			if name != "" {
				names = append(names, name)
			}
		}
		if idRaw, err := oleutil.GetProperty(category, "CategoryID"); err == nil {
			id := NormalizeGUID(variantString(idRaw))
			idRaw.Clear()
			if id != "" {
				ids = append(ids, id)
			}
		}
		category.Release()
	}
	return names, ids
}

func readCategoryNames(coll *ole.IDispatch) []string {
	countRaw, err := oleutil.GetProperty(coll, "Count")
	if err != nil {
		return nil
	}
	count := variantInt(countRaw)
	countRaw.Clear()
	out := make([]string, 0, count)
	for i := 0; i < count; i++ {
		itemRaw, err := collectionItem(coll, i)
		if err != nil {
			continue
		}
		category := itemRaw.ToIDispatch()
		if category == nil {
			itemRaw.Clear()
			continue
		}
		if nameRaw, err := oleutil.GetProperty(category, "Name"); err == nil {
			name := variantString(nameRaw)
			nameRaw.Clear()
			if name != "" {
				out = append(out, name)
			}
		}
		category.Release()
	}
	return out
}

// tuneSearcherForScan ajusta o escopo do IUpdateSearcher (IUpdateSearcher3).
//
// O escopo DEFAULT do WUA e "machine only" e EXCLUI atualizacoes por usuario
// (IUpdate4.PerUser). Ofertas de upgrade de versao no Windows 11 aparecem na
// UI do Windows Update no contexto do usuario; ao escanear como servico elas
// podem ficar de fora. Tentamos o escopo 4 (machine + all users); em WUA que
// nao suporta o propriedade o PutProperty falha e o escopo default permanece.
// Nao e fatal: o scan segue com o comportamento anterior.
func tuneSearcherForScan(searcher *ole.IDispatch) {
	if searcher == nil {
		return
	}
	_, _ = oleutil.PutProperty(searcher, "Online", true)
	_, _ = oleutil.PutProperty(searcher, "SearchScope", int32(4))
}

func scanWUA(opts ScanOptions) (*ScanResult, error) {
	started := time.Now().UTC()
	session, err := openSession()
	if err != nil {
		return nil, err
	}
	defer session.Close()

	searcher, err := session.searcher()
	if err != nil {
		return nil, ErrWuaUnavailable
	}
	defer searcher.Release()
	tuneSearcherForScan(searcher)

	pending, counters, err := searchPending(searcher, opts.IncludeHidden, opts.IncludeDrivers)
	if err != nil {
		return nil, err
	}
	// O WUA nao lista feature update/upgrade de versao no Windows 11. Usa a
	// avaliacao WaaS (registro do USO) para incluir o upgrade no inventario.
	if assessment, ok := readWaaSFeatureAssessment(); ok {
		pending = appendWaaSFeatureUpdate(pending, assessment, currentDisplayVersion())
	}

	res := &ScanResult{
		StartedAt: started,
		Source:    SourceWUA,
		Updates:   pending,
	}
	for _, update := range pending {
		if update.IsPending {
			res.PendingCount++
		}
	}
	// Nada e descartado em silencio: cada motivo aparece no SkippedReasons e
	// no log do chamador (inventory).
	for reason, count := range counters.Skipped {
		for i := 0; i < count; i++ {
			res.AddSkipped(reason)
		}
	}
	res.UnreadableErr = counters.UnreadableErr

	if opts.IncludeInstalled {
		installed, err := queryInstalledQFE()
		if err == nil {
			res.Updates = append(res.Updates, installed...)
			res.InstalledCount = len(installed)
		} else {
			// QFE falhou: o scan é PARCIAL. Sem este sinal o servidor poderia
			// interpretar "nenhuma instalada" como remoção e apagar o inventário.
			res.Partial = true
		}
	}

	res.PendingReboot = probePendingReboot()
	res.State = ProbeLocalState()
	res.State.RebootPending = res.PendingReboot.Pending
	res.State.RebootReasons = append([]string(nil), res.PendingReboot.Reasons...)
	res.DurationMs = time.Since(started).Milliseconds()
	return res, nil
}

// installWUA instala somente os alvos encontrados numa busca fresca.
func installWUA(targets []InstallTarget) (*InstallResult, error) {
	started := time.Now().UTC()
	session, err := openSession()
	if err != nil {
		return nil, err
	}
	defer session.Close()

	searcher, err := session.searcher()
	if err != nil {
		return nil, ErrWuaUnavailable
	}
	defer searcher.Release()

	// Busca SEM filtro de hidden/BrowseOnly: updates ocultos ou opcionais
	// (inclusive upgrades de versao) podem ter sido aprovados explicitamente.
	// Type='Software' mantem drivers fora (o modulo nao os inventaria) sem
	// custo de ler centenas de objetos COM. A selecao e por GUID/chave canonica.
	collection, err := searchUpdatesCollection(searcher, wuaPendingSoftwareCriteria)
	if err != nil {
		return nil, err
	}
	defer collection.Release()

	byKey, unreadable, err := collectUpdatesByKey(collection)
	if err != nil {
		return nil, err
	}
	// Libera as referências IDispatch dos updates depois de montar/instalar a
	// coleção (a coleção faz AddRef; sem isso vazaria uma referência por update).
	defer func() {
		for _, entry := range byKey {
			if entry.update != nil {
				entry.update.Release()
			}
		}
	}()

	// WU-07: alvos ausentes da busca de pendentes podem estar instalados ou ter
	// saido do catalogo. Consultamos a busca de INSTALADOS uma unica vez, e so
	// quando existe algum alvo ausente (custo zero no caminho feliz).
	installedKeys := map[string]bool(nil)
	for _, target := range targets {
		if _, ok := byKey[target.UpdateKey]; !ok {
			installedKeys = updateKeysFromSearch(session, wuaInstalledSoftwareCriteria)
			break
		}
	}

	result := &InstallResult{}
	targetColl, err := oleutil.CreateObject("Microsoft.Update.UpdateColl")
	if err != nil {
		return nil, ErrWuaUnavailable
	}
	targetDisp, err := targetColl.QueryInterface(ole.IID_IDispatch)
	targetColl.Release()
	if err != nil {
		return nil, ErrWuaUnavailable
	}
	defer targetDisp.Release()

	added := make([]InstallTarget, 0, len(targets))
	needsDownload := false
	for _, target := range targets {
		entry, ok := byKey[target.UpdateKey]
		if !ok {
			outcome, isFailure := ClassifyMissingTarget(target.UpdateKey, installedKeys, unreadable)
			if isFailure {
				result.Failed = append(result.Failed, outcome)
			} else {
				result.Skipped = append(result.Skipped, outcome)
			}
			continue
		}
		if _, err := oleutil.CallMethod(targetDisp, "Add", entry.update); err != nil {
			result.Failed = append(result.Failed, InstallOutcome{
				UpdateKey: target.UpdateKey,
				KBID:      entry.kbID,
				Error:     "falha ao montar a colecao de updates",
			})
			continue
		}
		added = append(added, target)
		if !entry.isDownloaded {
			needsDownload = true
		}
	}

	if len(added) == 0 {
		result.DurationMs = time.Since(started).Milliseconds()
		return result, nil
	}

	if needsDownload {
		downloaderRaw, err := oleutil.CallMethod(session.session, "CreateUpdateDownloader")
		if err == nil {
			downloader := downloaderRaw.ToIDispatch()
			if downloader == nil {
				downloaderRaw.Clear()
			} else {
				if _, putErr := oleutil.PutProperty(downloader, "Updates", targetDisp); putErr == nil {
					downResultRaw, dlErr := oleutil.CallMethod(downloader, "Download")
					if dlErr == nil {
						downloadResult := downResultRaw.ToIDispatch()
						code := readOperationResultCode(downloadResult)
						if downloadResult != nil {
							downloadResult.Release()
						} else {
							downResultRaw.Clear()
						}
						if code != wuaResultSucceeded && code != wuaResultSucceededWithErr {
							// Falha de download: nao tenta instalar (evita erro obscuro).
							for _, target := range added {
								result.Failed = append(result.Failed, InstallOutcome{
									UpdateKey:  target.UpdateKey,
									ResultCode: code,
									Error:      "download da atualizacao falhou",
								})
							}
							result.DurationMs = time.Since(started).Milliseconds()
							return result, nil
						}
					}
				}
				downloader.Release()
			}
		}
	}

	// WU-17/D16: UM checkpoint por comando, preguicoso, antes da primeira
	// mutacao real. Definitions nao geram checkpoint; Server e pulado. Falha de
	// checkpoint NAO aborta a instalacao (fica registrada no resultado).
	{
		categories := make([][]string, 0, len(added))
		for _, target := range added {
			if entry, ok := byKey[target.UpdateKey]; ok {
				categories = append(categories, entry.categories)
			}
		}
		if NeedsRestorePoint(categories) {
			if rpErr := createRestorePoint(restorePointDescription); rpErr != nil {
				result.RestorePointError = rpErr.Error()
			} else {
				result.RestorePointCreated = true
			}
		}
	}

	installerRaw, err := oleutil.CallMethod(session.session, "CreateUpdateInstaller")
	if err != nil {
		return nil, ErrWuaUnavailable
	}
	installer := installerRaw.ToIDispatch()
	if installer == nil {
		installerRaw.Clear()
		return nil, ErrWuaUnavailable
	}
	defer installer.Release()
	if _, err := oleutil.PutProperty(installer, "Updates", targetDisp); err != nil {
		return nil, ErrWuaUnavailable
	}

	installResultRaw, err := oleutil.CallMethod(installer, "Install")
	if err != nil {
		return nil, err
	}
	installResult := installResultRaw.ToIDispatch()
	if installResult == nil {
		installResultRaw.Clear()
		return nil, ErrWuaUnavailable
	}
	defer installResult.Release()
	result.RebootRequired = variantBoolMust(installResult, "RebootRequired")

	for i, target := range added {
		outcome := InstallOutcome{UpdateKey: target.UpdateKey}
		if entry, ok := byKey[target.UpdateKey]; ok {
			outcome.KBID = entry.kbID
		}
		updateResultRaw, err := oleutil.CallMethod(installResult, "GetUpdateResult", i)
		if err != nil {
			outcome.ResultCode = wuaResultFailed
			outcome.Error = "resultado por update indisponivel"
			result.Failed = append(result.Failed, outcome)
			continue
		}
		updateResult := updateResultRaw.ToIDispatch()
		code := readOperationResultCode(updateResult)
		reboot := false
		if updateResult != nil {
			reboot = variantBoolMust(updateResult, "RebootRequired")
			updateResult.Release()
		} else {
			updateResultRaw.Clear()
		}
		outcome.ResultCode = code
		outcome.RebootRequired = reboot
		if code == wuaResultSucceeded || code == wuaResultSucceededWithErr {
			result.Installed = append(result.Installed, outcome)
		} else {
			outcome.Error = "instalacao retornou codigo " + fmt.Sprint(code)
			result.Failed = append(result.Failed, outcome)
		}
		if reboot {
			result.RebootRequired = true
		}
	}

	// WU-07: verificacao pos-instalacao. Se o update continua aparecendo como
	// pendente depois de reportar sucesso, ele foi aceito mas ainda nao foi
	// efetivado: normalmente falta reiniciar. Antes isso passava como sucesso
	// silencioso e o operador via o mesmo KB pendente no scan seguinte.
	if len(result.Installed) > 0 {
		if pendingKeys := pendingKeysAfterInstall(session); len(pendingKeys) > 0 {
			for _, key := range StillPendingAfterInstall(result.Installed, pendingKeys) {
				for i := range result.Installed {
					if result.Installed[i].UpdateKey != key {
						continue
					}
					result.Installed[i].RebootRequired = true
					result.Installed[i].Error = "instalado, mas ainda listado como pendente: reinicio necessario"
					result.RebootRequired = true
				}
			}
		}
	}

	result.DurationMs = time.Since(started).Milliseconds()
	return result, nil
}

type updateEntry struct {
	update       *ole.IDispatch
	kbID         string
	isDownloaded bool
	// categories alimenta a decisao de restore point (WU-17): definitions nao
	// precisam de checkpoint.
	categories []string
}

func collectUpdatesByKey(collection *ole.IDispatch) (map[string]updateEntry, int, error) {
	countRaw, err := oleutil.GetProperty(collection, "Count")
	if err != nil {
		return nil, 0, err
	}
	count := variantInt(countRaw)
	countRaw.Clear()
	out := make(map[string]updateEntry, count)
	// unreadable conta itens que NAO puderam ser lidos. Um alvo ausente do mapa
	// so pode ser tratado como "nao oferecido" quando unreadable == 0 (WU-07):
	// caso contrario o update pode ser justamente um dos ilegiveis.
	unreadable := 0
	for i := 0; i < count; i++ {
		itemRaw, err := collectionItem(collection, i)
		if err != nil {
			unreadable++
			continue
		}
		item := itemRaw.ToIDispatch()
		if item == nil {
			itemRaw.Clear()
			unreadable++
			continue
		}
		update, skip := readUpdate(item)
		if skip {
			item.Release()
			unreadable++
			continue
		}
		// guarda a referencia para montar a colecao de instalacao
		out[update.UpdateKey] = updateEntry{
			update:       item,
			kbID:         update.KBID,
			isDownloaded: update.IsDownloaded,
			categories:   update.Categories,
		}
	}
	return out, unreadable, nil
}

// updateKeysFromSearch executa uma busca WUA e devolve o conjunto de chaves
// canonicas. Usado para saber se um alvo ausente da busca de pendentes ja esta
// instalado (WU-07). Falha devolve mapa vazio: sem essa informacao o alvo vira
// skip "not_offered", que e o comportamento mais conservador.
func updateKeysFromSearch(session *wuaSession, criteria string) map[string]bool {
	searcher, err := session.searcher()
	if err != nil {
		return map[string]bool{}
	}
	defer searcher.Release()

	collection, err := searchUpdatesCollection(searcher, criteria)
	if err != nil {
		return map[string]bool{}
	}
	defer collection.Release()

	byKey, _, err := collectUpdatesByKey(collection)
	if err != nil {
		return map[string]bool{}
	}
	keys := make(map[string]bool, len(byKey))
	for key, entry := range byKey {
		keys[key] = true
		if entry.update != nil {
			entry.update.Release()
		}
	}
	return keys
}

// pendingKeysAfterInstall refaz a busca de pendentes para a verificacao
// pos-instalacao (WU-07).
func pendingKeysAfterInstall(session *wuaSession) map[string]bool {
	// Mesma busca do inventario: opcionais/upgrades tambem contam como
	// pendentes depois de uma instalacao.
	return updateKeysFromSearch(session, wuaPendingSoftwareCriteria)
}

func hideWUA(updateKeys []string, hidden bool) (int, error) {
	session, err := openSession()
	if err != nil {
		return 0, err
	}
	defer session.Close()
	searcher, err := session.searcher()
	if err != nil {
		return 0, ErrWuaUnavailable
	}
	defer searcher.Release()

	collection, err := searchUpdatesCollection(searcher, wuaPendingSoftwareCriteria)
	if err != nil {
		return 0, err
	}
	defer collection.Release()

	byKey, _, err := collectUpdatesByKey(collection)
	if err != nil {
		return 0, err
	}
	defer func() {
		for _, entry := range byKey {
			if entry.update != nil {
				entry.update.Release()
			}
		}
	}()

	changed := 0
	for _, key := range updateKeys {
		entry, ok := byKey[key]
		if !ok {
			continue
		}
		if _, err := oleutil.PutProperty(entry.update, "IsHidden", hidden); err != nil {
			continue
		}
		changed++
	}
	return changed, nil
}

// ---- VARIANT helpers -------------------------------------------------------

func variantString(v *ole.VARIANT) string {
	if v == nil {
		return ""
	}
	switch v.VT {
	case ole.VT_BSTR:
		return strings.TrimSpace(v.ToString())
	case ole.VT_NULL, ole.VT_EMPTY:
		return ""
	}
	value := v.Value()
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func variantBool(v *ole.VARIANT) bool {
	if v == nil {
		return false
	}
	if b, ok := v.Value().(bool); ok {
		return b
	}
	return v.Val != 0
}

func variantInt(v *ole.VARIANT) int {
	if v == nil {
		return 0
	}
	switch value := v.Value().(type) {
	case int32:
		return int(value)
	case int64:
		return int(value)
	case int:
		return value
	case uint32:
		return int(value)
	case uint16:
		return int(value)
	}
	return int(v.Val)
}

// variantInt64 cobre os tipos inteiros que o WUA devolve em propriedades como
// MaxDownloadSize: VT_I4/VT_UI4, VT_I8/VT_UI8 e os equivalentes Go.
func variantInt64(v *ole.VARIANT) int64 {
	if v == nil {
		return 0
	}
	// Nao usar Value() para VT_I4/VT_UI4: ele devolve int32 e truncaria em
	// negativo um tamanho acima de 2 GiB. Val ja e int64 e carrega o inteiro
	// sem sinal para esses tipos.
	switch v.VT {
	case ole.VT_I4, ole.VT_UI4, ole.VT_I8, ole.VT_UI8, ole.VT_INT, ole.VT_UINT:
		return int64(v.Val)
	}
	switch value := v.Value().(type) {
	case int64:
		return value
	case int32:
		return int64(value)
	case int:
		return int64(value)
	case uint64:
		return int64(value)
	case uint32:
		return int64(value)
	case uint16:
		return int64(value)
	case uint8:
		return int64(value)
	}
	return int64(v.Val)
}

func variantTime(v *ole.VARIANT) *time.Time {
	if v == nil {
		return nil
	}
	if value, ok := v.Value().(time.Time); ok {
		utc := value.UTC()
		return &utc
	}
	return nil
}

func variantBoolMust(disp *ole.IDispatch, property string) bool {
	raw, err := oleutil.GetProperty(disp, property)
	if err != nil {
		return false
	}
	defer raw.Clear()
	return variantBool(raw)
}

func readOperationResultCode(disp *ole.IDispatch) int {
	if disp == nil {
		return wuaResultFailed
	}
	raw, err := oleutil.GetProperty(disp, "ResultCode")
	if err != nil {
		return wuaResultFailed
	}
	defer raw.Clear()
	return variantInt(raw)
}

func truncate(value string, max int) string {
	value = strings.TrimSpace(value)
	if max > 0 && len(value) > max {
		return value[:max]
	}
	return value
}
