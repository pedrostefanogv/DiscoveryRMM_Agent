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
	if strings.EqualFold(windowsUpdateServiceState(), "disabled") {
		return nil, ErrServiceDisabled
	}
	return withOp(c, ctx, c.opts.ScanTimeout, func() (*ScanResult, error) {
		return scanWUA(opts)
	})
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

func pendingCriteria(includeHidden, includeDrivers bool) []string {
	base := "IsInstalled=0 and Type='Software'"
	if includeDrivers {
		base = "IsInstalled=0"
	}
	if includeHidden {
		return []string{base + " and BrowseOnly=0", base}
	}
	return []string{base + " and IsHidden=0 and BrowseOnly=0", base + " and IsHidden=0"}
}

// searchPending executa a busca de pendentes tentando variantes de criterio
// (BrowseOnly pode nao existir em WUA antigo).
func searchPending(searcher *ole.IDispatch, includeHidden, includeDrivers bool) ([]Update, error) {
	var lastErr error
	for _, criteria := range pendingCriteria(includeHidden, includeDrivers) {
		collection, err := searchUpdatesCollection(searcher, criteria)
		if err != nil {
			lastErr = err
			var oleErr *ole.OleError
			if errors.As(err, &oleErr) && oleErr.Code() == hresultInvalidCriteria {
				continue
			}
			return nil, err
		}
		updates, err := readUpdates(collection, includeDrivers)
		collection.Release()
		if err != nil {
			return nil, err
		}
		return updates, nil
	}
	return nil, lastErr
}

func readUpdates(collection *ole.IDispatch, includeDrivers bool) ([]Update, error) {
	countRaw, err := oleutil.GetProperty(collection, "Count")
	if err != nil {
		return nil, err
	}
	count := variantInt(countRaw)
	countRaw.Clear()
	if count < 0 {
		count = 0
	}
	out := make([]Update, 0, count)
	for i := 0; i < count; i++ {
		itemRaw, err := oleutil.CallMethod(collection, "Item", i)
		if err != nil {
			continue
		}
		item := itemRaw.ToIDispatch()
		if item == nil {
			itemRaw.Clear()
			continue
		}
		update, skip := readUpdate(item)
		item.Release()
		if skip {
			continue
		}
		if !includeDrivers && isDriverOrUpgrade(update) {
			continue
		}
		out = append(out, update)
	}
	return out, nil
}

func readUpdate(update *ole.IDispatch) (Update, bool) {
	u := Update{Source: SourceWUA}
	title, description, severity, supportURL := "", "", "", ""
	var isDownloaded, isMandatory, isHidden, rebootRequired bool
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
	if raw, err := oleutil.GetProperty(update, "Categories"); err == nil {
		if coll := raw.ToIDispatch(); coll != nil {
			categories = readCategoryNames(coll)
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
	u.Severity = severity
	u.SupportURL = truncate(supportURL, 1000)
	u.IsPending = true
	u.IsDownloaded = isDownloaded
	u.IsMandatory = isMandatory
	u.IsHidden = isHidden
	u.RequiresReboot = rebootRequired
	u.PublishedAt = lastDeployed
	u.LastDeployedAt = lastDeployed
	u.Categories = categories
	u.Classification = classify(categories)
	return u, false
}

func isDriverOrUpgrade(u Update) bool {
	for _, category := range u.Categories {
		name := strings.ToLower(strings.TrimSpace(category))
		if name == "upgrades" || name == "drivers" {
			return true
		}
	}
	return false
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
		itemRaw, err := oleutil.CallMethod(coll, "Item", i)
		if err != nil {
			continue
		}
		out = append(out, variantString(itemRaw))
		itemRaw.Clear()
	}
	return out
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
		itemRaw, err := oleutil.CallMethod(coll, "Item", i)
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

	pending, err := searchPending(searcher, opts.IncludeHidden, opts.IncludeDrivers)
	if err != nil {
		return nil, err
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

	// Busca SEM filtro de hidden: updates ocultos podem ter sido aprovados
	// explicitamente. A selecao e sempre por GUID/chave canonica.
	collection, err := searchUpdatesCollection(searcher, "IsInstalled=0 and Type='Software'")
	if err != nil {
		return nil, err
	}
	defer collection.Release()

	byKey, err := collectUpdatesByKey(collection)
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
			result.NotFound = append(result.NotFound, target.UpdateKey)
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

	result.DurationMs = time.Since(started).Milliseconds()
	return result, nil
}

type updateEntry struct {
	update       *ole.IDispatch
	kbID         string
	isDownloaded bool
}

func collectUpdatesByKey(collection *ole.IDispatch) (map[string]updateEntry, error) {
	countRaw, err := oleutil.GetProperty(collection, "Count")
	if err != nil {
		return nil, err
	}
	count := variantInt(countRaw)
	countRaw.Clear()
	out := make(map[string]updateEntry, count)
	for i := 0; i < count; i++ {
		itemRaw, err := oleutil.CallMethod(collection, "Item", i)
		if err != nil {
			continue
		}
		item := itemRaw.ToIDispatch()
		if item == nil {
			itemRaw.Clear()
			continue
		}
		update, skip := readUpdate(item)
		if skip {
			item.Release()
			continue
		}
		// guarda a referencia para montar a colecao de instalacao
		out[update.UpdateKey] = updateEntry{update: item, kbID: update.KBID, isDownloaded: update.IsDownloaded}
	}
	return out, nil
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

	collection, err := searchUpdatesCollection(searcher, "IsInstalled=0 and Type='Software'")
	if err != nil {
		return 0, err
	}
	defer collection.Release()

	byKey, err := collectUpdatesByKey(collection)
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
