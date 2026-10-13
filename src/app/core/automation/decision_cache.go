package automation

import (
	"context"
	"sync"
	"time"
)

// decisionListCacheTTL é a janela em que o resultado de "winget list/upgrade" é
// reaproveitado pelas decisões de execução (pré-carga P2P, pré-check do gatilho
// e executor).
//
// Sem isso, cada tarefa de pacote dispara 1-2 processos do winget por ciclo:
// com 11 tarefas e 2 gatilhos passava de 20 spawns por policy-sync, e cada
// spawn custa segundos (foi o churn observado no incidente do Python).
const decisionListCacheTTL = 90 * time.Second

// decisionListCache envolve o PackageManager do serviço de automação para
// memoizar as listagens consumidas pelo decisor (decideWingetAction /
// ShouldPreloadPackage). Instalação, atualização e remoção invalidam o cache,
// então a próxima decisão enxerga o estado novo.
//
// O wrapper também expõe richPackageLister (delegando ao inner quando
// suportado): sem isso o decisor cairia para a tabela tabular, cujos Ids vêm
// truncados e geram falso negativo.
type decisionListCache struct {
	PackageManager

	mu         sync.Mutex
	ttl        time.Duration
	installed  listCacheEntry
	upgradable listCacheEntry
	rich       richPackageLister
}

type listCacheEntry struct {
	out string
	err error
	at  time.Time
	ok  bool
}

// newDecisionListCache devolve nil quando não há gerenciador — os decisores já
// tratam nil como "sem evidência, não pula".
// Devolve a interface (e não o ponteiro) de propósito: um *decisionListCache
// nil guardado numa interface PackageManager não seria "== nil", e os decisores
// passariam a enxergar um gerenciador inexistente como presente.
func newDecisionListCache(inner PackageManager) PackageManager {
	if inner == nil {
		return nil
	}
	c := &decisionListCache{PackageManager: inner, ttl: decisionListCacheTTL}
	if rich, ok := inner.(richPackageLister); ok {
		c.rich = rich
	}
	return c
}

// Invalidate descarta as listagens memoizadas. Chamado após qualquer operação
// que mude o estado de software da máquina.
func (c *decisionListCache) Invalidate() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.installed = listCacheEntry{}
	c.upgradable = listCacheEntry{}
	c.mu.Unlock()
}

// cached devolve o valor memoizado ou executa o loader. O lock cobre a chamada
// inteira de propósito: chamadas concorrentes compartilham um único spawn do
// winget em vez de rodarem em paralelo.
func (c *decisionListCache) cached(entry *listCacheEntry, load func() (string, error)) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry.ok && time.Since(entry.at) < c.ttl {
		return entry.out, entry.err
	}
	out, err := load()
	*entry = listCacheEntry{out: out, err: err, at: time.Now(), ok: true}
	return out, err
}

func (c *decisionListCache) ListInstalled(ctx context.Context) (string, error) {
	return c.cached(&c.installed, func() (string, error) { return c.PackageManager.ListInstalled(ctx) })
}

func (c *decisionListCache) ListUpgradable(ctx context.Context) (string, error) {
	return c.cached(&c.upgradable, func() (string, error) { return c.PackageManager.ListUpgradable(ctx) })
}

// ListInstalledRich usa o JSON do winget quando o inner suporta; sem suporte a
// decisão cai para a listagem tabular (mesmo caminho de antes do cache).
func (c *decisionListCache) ListInstalledRich(ctx context.Context) (string, error) {
	if c.rich == nil {
		return c.ListInstalled(ctx)
	}
	return c.cached(&c.installed, func() (string, error) { return c.rich.ListInstalledRich(ctx) })
}

func (c *decisionListCache) ListUpgradableRich(ctx context.Context) (string, error) {
	if c.rich == nil {
		return c.ListUpgradable(ctx)
	}
	return c.cached(&c.upgradable, func() (string, error) { return c.rich.ListUpgradableRich(ctx) })
}

func (c *decisionListCache) Install(ctx context.Context, id string) (string, error) {
	out, err := c.PackageManager.Install(ctx, id)
	c.Invalidate()
	return out, err
}

func (c *decisionListCache) Upgrade(ctx context.Context, id string) (string, error) {
	out, err := c.PackageManager.Upgrade(ctx, id)
	c.Invalidate()
	return out, err
}

func (c *decisionListCache) Uninstall(ctx context.Context, id string) (string, error) {
	out, err := c.PackageManager.Uninstall(ctx, id)
	c.Invalidate()
	return out, err
}

func (c *decisionListCache) UpgradeAll(ctx context.Context) (string, error) {
	out, err := c.PackageManager.UpgradeAll(ctx)
	c.Invalidate()
	return out, err
}
