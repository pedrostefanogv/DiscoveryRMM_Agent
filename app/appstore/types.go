package appstore

import (
	"sync"
	"time"
)

// InstallationType representa os tipos suportados no app-store do agent.
type InstallationType string

const (
	InstallationWinget     InstallationType = "Winget"
	InstallationChocolatey InstallationType = "Chocolatey"
)

// Item representa um item permitido retornado por /api/agent-auth/me/app-store.
type Item struct {
	InstallationType    string            `json:"installationType"`
	PackageID           string            `json:"packageId"`
	Name                string            `json:"name"`
	Description         string            `json:"description"`
	IconURL             string            `json:"iconUrl"`
	Publisher           string            `json:"publisher"`
	Version             string            `json:"version"`
	InstallCommand      string            `json:"installCommand"`
	InstallerURLsByArch map[string]string `json:"installerUrlsByArch"`
	AutoUpdateEnabled   bool              `json:"autoUpdateEnabled"`
	SourceScope         string            `json:"sourceScope"`
}

// Response representa o envelope do endpoint /api/agent-auth/me/app-store.
type Response struct {
	InstallationType string `json:"installationType"`
	Count            int    `json:"count"`
	Items            []Item `json:"items"`
}

// EffectivePolicy consolida os itens permitidos para todos os tipos suportados.
type EffectivePolicy struct {
	Items     []Item `json:"items"`
	FetchedAt string `json:"fetchedAt"`
}

type PolicyCache struct {
	mu       sync.RWMutex
	policy   EffectivePolicy
	loadedAt time.Time
	loaded   bool
}

func (c *PolicyCache) Get(maxAge time.Duration) (EffectivePolicy, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.loaded {
		return EffectivePolicy{}, false
	}
	if maxAge > 0 && time.Since(c.loadedAt) > maxAge {
		return EffectivePolicy{}, false
	}
	return c.policy, true
}

func (c *PolicyCache) get(maxAge time.Duration) (EffectivePolicy, bool) {
	return c.Get(maxAge)
}

func (c *PolicyCache) Set(policy EffectivePolicy) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.policy = policy
	c.loadedAt = time.Now()
	c.loaded = true
}

func (c *PolicyCache) set(policy EffectivePolicy) {
	c.Set(policy)
}

func (c *PolicyCache) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.policy = EffectivePolicy{}
	c.loadedAt = time.Time{}
	c.loaded = false
}

func (c *PolicyCache) invalidate() {
	c.Invalidate()
}
