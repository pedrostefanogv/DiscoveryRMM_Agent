package supportmeta

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// AgentInfo holds key identifiers resolved from the server for the connected agent.
type AgentInfo struct {
	AgentID  string `json:"agentId"`
	ClientID string `json:"clientId"`
	SiteID   string `json:"siteId"`
	Hostname string `json:"hostname"`
	Name     string `json:"displayName"`
}

// ChatConfig is the frontend-facing AI configuration.
type ChatConfig struct {
	Endpoint     string `json:"endpoint"`
	APIKey       string `json:"apiKey"`
	Model        string `json:"model"`
	SystemPrompt string `json:"systemPrompt"`
	MaxTokens    int    `json:"maxTokens"`
}

// ChatMessage is a single message for the frontend.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// APIWorkflowState is the workflow state embedded in a ticket response.
type APIWorkflowState struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Color        string `json:"color"`
	IsInitial    bool   `json:"isInitial"`
	IsFinal      bool   `json:"isFinal"`
	DisplayOrder int    `json:"displayOrder"`
}

func (w *APIWorkflowState) UnmarshalJSON(data []byte) error {
	type alias APIWorkflowState
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	var out alias
	out.ID = strings.TrimSpace(fmt.Sprint(raw["id"]))
	out.Name = strings.TrimSpace(fmt.Sprint(raw["name"]))
	out.Color = strings.TrimSpace(fmt.Sprint(raw["color"]))
	out.IsInitial = toBool(raw["isInitial"], raw["initial"])
	out.IsFinal = toBool(raw["isFinal"], raw["final"], raw["isTerminal"])
	out.DisplayOrder = toInt(raw["displayOrder"], raw["order"], raw["sortOrder"], raw["position"])
	*w = APIWorkflowState(out)
	return nil
}

// TicketPriority normalizes priority values from API responses.
type TicketPriority int

func (p *TicketPriority) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		*p = TicketPriority(0)
		return nil
	}

	var n int
	if err := json.Unmarshal(data, &n); err == nil {
		*p = TicketPriority(normalizePriority(n))
		return nil
	}

	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*p = TicketPriority(priorityLabelToInt(s))
		return nil
	}

	return fmt.Errorf("prioridade inválida")
}

// APITicket is the ticket representation returned by the remote API.
type APITicket struct {
	ID            string            `json:"id"`
	Title         string            `json:"title"`
	Description   string            `json:"description"`
	Priority      TicketPriority    `json:"priority"`
	Category      *string           `json:"category,omitempty"`
	AgentID       *string           `json:"agentId,omitempty"`
	ClientID      string            `json:"clientId"`
	SiteID        *string           `json:"siteId,omitempty"`
	CreatedAt     string            `json:"createdAt"`
	WorkflowState *APIWorkflowState `json:"workflowState,omitempty"`
	Rating        *int              `json:"rating,omitempty"`
	RatedAt       *string           `json:"ratedAt,omitempty"`
	RatedBy       *string           `json:"ratedBy,omitempty"`
}

// TicketComment is a comment on a ticket.
type TicketComment struct {
	ID         string `json:"id"`
	Author     string `json:"author"`
	Content    string `json:"content"`
	IsInternal bool   `json:"isInternal"`
	CreatedAt  string `json:"createdAt"`
}

// CreateTicketInput is the frontend-facing request to create a ticket.
type CreateTicketInput struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Priority    int    `json:"priority"`
	Category    string `json:"category"`
}

// CloseTicketInput is the frontend-facing request to close a ticket.
type CloseTicketInput struct {
	Rating          *int   `json:"rating,omitempty"`
	Comment         string `json:"comment,omitempty"`
	WorkflowStateID string `json:"workflowStateId,omitempty"`
}

// KnowledgeArticle represents a knowledge base article for support guidance.
type KnowledgeArticle struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Category    string   `json:"category"`
	Summary     string   `json:"summary"`
	Content     string   `json:"content"`
	Tags        []string `json:"tags"`
	Author      string   `json:"author"`
	Scope       string   `json:"scope"`
	PublishedAt string   `json:"publishedAt"`
	Difficulty  string   `json:"difficulty"`
	ReadTimeMin int      `json:"readTimeMin"`
	UpdatedAt   string   `json:"updatedAt"`
}

type AgentInfoCache struct {
	mu     sync.RWMutex
	info   AgentInfo
	loaded bool
}

func (c *AgentInfoCache) Get() (AgentInfo, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.info, c.loaded
}

func (c *AgentInfoCache) Set(info AgentInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.info = info
	c.loaded = true
}

func (c *AgentInfoCache) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loaded = false
}

func toInt(values ...any) int {
	for _, v := range values {
		switch n := v.(type) {
		case float64:
			return int(n)
		case float32:
			return int(n)
		case int:
			return n
		case int64:
			return int(n)
		case json.Number:
			if i, err := n.Int64(); err == nil {
				return int(i)
			}
		case string:
			s := strings.TrimSpace(n)
			if s == "" {
				continue
			}
			var parsed int
			if _, err := fmt.Sscanf(s, "%d", &parsed); err == nil {
				return parsed
			}
		}
	}
	return 0
}

func toBool(values ...any) bool {
	for _, v := range values {
		switch b := v.(type) {
		case bool:
			return b
		case string:
			s := strings.ToLower(strings.TrimSpace(b))
			if s == "true" || s == "1" || s == "yes" || s == "sim" {
				return true
			}
			if s == "false" || s == "0" || s == "no" || s == "nao" || s == "não" {
				return false
			}
		case float64:
			return b != 0
		case int:
			return b != 0
		}
	}
	return false
}

func normalizePriority(v int) int {
	if v < 1 || v > 4 {
		return 2
	}
	return v
}

func priorityLabelToInt(label string) int {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "1", "low", "baixa":
		return 1
	case "3", "high", "alta":
		return 3
	case "4", "critical", "critica", "crítica":
		return 4
	case "2", "medium", "media", "média":
		fallthrough
	default:
		return 2
	}
}
