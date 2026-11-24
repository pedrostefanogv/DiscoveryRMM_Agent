package screen

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// MaxAnnotationPayloadBytes limita o tamanho de um comando .annotate (16 KB).
	MaxAnnotationPayloadBytes = 16 * 1024
	// MaxActiveAnnotations limita quantas formas podem coexistir na tela.
	MaxActiveAnnotations = 200
	// MaxAnnotationPoints limita a quantidade de pontos em um traço freehand/laser.
	MaxAnnotationPoints = 256
	// MaxAnnotationTTL limita o tempo máximo de vida de uma anotação temporária.
	MaxAnnotationTTL = 5 * time.Minute
	// AnnotationFadeWindow é a janela final de fade-out antes da expiração.
	AnnotationFadeWindow = 500 * time.Millisecond

	defaultAnnotationColor uint32  = 0xFF3B30
	defaultAnnotationWidth float64 = 4
	minAnnotationWidth     float64 = 1
	maxAnnotationWidth     float64 = 24
)

// AnnotationTool identifica o tipo geométrico da anotação.
type AnnotationTool string

const (
	AnnotationToolArrow    AnnotationTool = "arrow"
	AnnotationToolRect     AnnotationTool = "rect"
	AnnotationToolEllipse  AnnotationTool = "ellipse"
	AnnotationToolFreehand AnnotationTool = "freehand"
	AnnotationToolLaser    AnnotationTool = "laser"
)

// AnnotationPoint representa uma coordenada normalizada [0.0 .. 1.0] relativa
// ao monitor capturado.
type AnnotationPoint struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// AnnotationCommand é o payload JSON recebido no subject .annotate.
type AnnotationCommand struct {
	Op     string            `json:"op"`
	ID     string            `json:"id,omitempty"`
	Tool   string            `json:"tool,omitempty"`
	Color  string            `json:"color,omitempty"`
	Width  float64           `json:"width,omitempty"`
	TTLMs  int               `json:"ttlMs,omitempty"`
	Points []AnnotationPoint `json:"points,omitempty"`
}

// AnnotationShape representa uma anotação validada pronta para renderização.
type AnnotationShape struct {
	ID        string
	Tool      AnnotationTool
	ColorRGB  uint32
	Width     float64
	Points    []AnnotationPoint
	CreatedAt time.Time
	ExpiresAt time.Time
	Alpha     float64 // 0..1 calculado em Snapshot
}

// AnnotationStore gerencia o conjunto ativo de anotações de forma thread-safe.
type AnnotationStore struct {
	mu     sync.Mutex
	shapes []AnnotationShape
}

// NewAnnotationStore cria um novo store de anotações vazio.
func NewAnnotationStore() *AnnotationStore {
	return &AnnotationStore{}
}

// Apply valida e aplica um comando JSON bruto no store.
// Retorna changed=true quando o conjunto visível foi alterado.
func (s *AnnotationStore) Apply(raw []byte, now time.Time) (bool, error) {
	if len(raw) == 0 {
		return false, errors.New("payload de anotação vazio")
	}
	if len(raw) > MaxAnnotationPayloadBytes {
		return false, fmt.Errorf("payload de anotação excede %d bytes (%d)", MaxAnnotationPayloadBytes, len(raw))
	}

	var cmd AnnotationCommand
	if err := json.Unmarshal(raw, &cmd); err != nil {
		return false, fmt.Errorf("json de anotação inválido: %w", err)
	}

	op := strings.ToLower(strings.TrimSpace(cmd.Op))
	switch op {
	case "add":
		shape, err := buildAnnotationShape(cmd, now)
		if err != nil {
			return false, err
		}
		s.mu.Lock()
		s.pruneLocked(now)
		if len(s.shapes) >= MaxActiveAnnotations {
			drop := len(s.shapes) - MaxActiveAnnotations + 1
			s.shapes = append([]AnnotationShape(nil), s.shapes[drop:]...)
		}
		s.shapes = append(s.shapes, shape)
		s.mu.Unlock()
		return true, nil

	case "undo":
		targetID := strings.TrimSpace(cmd.ID)
		s.mu.Lock()
		changed := s.pruneLocked(now)
		if targetID != "" {
			for i := len(s.shapes) - 1; i >= 0; i-- {
				if s.shapes[i].ID == targetID {
					s.shapes = append(s.shapes[:i], s.shapes[i+1:]...)
					changed = true
					break
				}
			}
		} else if len(s.shapes) > 0 {
			s.shapes = s.shapes[:len(s.shapes)-1]
			changed = true
		}
		s.mu.Unlock()
		return changed, nil

	case "clear":
		return s.Clear(), nil

	default:
		return false, fmt.Errorf("operação de anotação desconhecida: %q", cmd.Op)
	}
}

// Clear remove todas as anotações ativas.
func (s *AnnotationStore) Clear() bool {
	s.mu.Lock()
	changed := len(s.shapes) > 0
	s.shapes = nil
	s.mu.Unlock()
	return changed
}

// Prune remove anotações expiradas e informa se algo foi removido.
func (s *AnnotationStore) Prune(now time.Time) bool {
	s.mu.Lock()
	changed := s.pruneLocked(now)
	s.mu.Unlock()
	return changed
}

// Snapshot devolve uma cópia das formas ativas com alpha calculado e o
// intervalo até a próxima atualização necessária (nextWake):
//   - 0 quando não há formas com expiração agendada;
//   - 33ms quando alguma forma já está na janela de fade-out;
//   - o tempo restante até a primeira forma entrar na janela de fade-out.
func (s *AnnotationStore) Snapshot(now time.Time) ([]AnnotationShape, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.pruneLocked(now)
	if len(s.shapes) == 0 {
		return nil, 0
	}

	const minWake = 33 * time.Millisecond
	out := make([]AnnotationShape, len(s.shapes))
	var nextWake time.Duration

	for i, sh := range s.shapes {
		cp := sh
		cp.Points = append([]AnnotationPoint(nil), sh.Points...)
		cp.Alpha = 1.0
		if !sh.ExpiresAt.IsZero() {
			rem := sh.ExpiresAt.Sub(now)
			var wake time.Duration
			if rem <= AnnotationFadeWindow {
				if rem <= 0 {
					cp.Alpha = 0
				} else {
					cp.Alpha = float64(rem) / float64(AnnotationFadeWindow)
				}
				wake = minWake
			} else {
				wake = rem - AnnotationFadeWindow
				if wake < minWake {
					wake = minWake
				}
			}
			if nextWake == 0 || wake < nextWake {
				nextWake = wake
			}
		}
		out[i] = cp
	}
	return out, nextWake
}

func (s *AnnotationStore) pruneLocked(now time.Time) bool {
	if len(s.shapes) == 0 {
		return false
	}
	kept := s.shapes[:0]
	removed := false
	for _, sh := range s.shapes {
		if !sh.ExpiresAt.IsZero() && !now.Before(sh.ExpiresAt) {
			removed = true
			continue
		}
		kept = append(kept, sh)
	}
	if removed {
		if len(kept) == 0 {
			s.shapes = nil
		} else {
			s.shapes = kept
		}
	}
	return removed
}

func buildAnnotationShape(cmd AnnotationCommand, now time.Time) (AnnotationShape, error) {
	tool := AnnotationTool(strings.ToLower(strings.TrimSpace(cmd.Tool)))
	switch tool {
	case AnnotationToolArrow, AnnotationToolRect, AnnotationToolEllipse, AnnotationToolFreehand, AnnotationToolLaser:
	default:
		return AnnotationShape{}, fmt.Errorf("ferramenta de anotação inválida: %q", cmd.Tool)
	}

	if len(cmd.Points) < 2 {
		return AnnotationShape{}, errors.New("anotação requer ao menos 2 pontos")
	}

	pts := cmd.Points
	if len(pts) > MaxAnnotationPoints {
		pts = downsamplePoints(pts, MaxAnnotationPoints)
	} else {
		cp := make([]AnnotationPoint, len(pts))
		copy(cp, pts)
		pts = cp
	}
	for i := range pts {
		pts[i].X = clamp01(pts[i].X)
		pts[i].Y = clamp01(pts[i].Y)
	}

	width := cmd.Width
	if math.IsNaN(width) || math.IsInf(width, 0) || width <= 0 {
		width = defaultAnnotationWidth
	} else if width < minAnnotationWidth {
		width = minAnnotationWidth
	} else if width > maxAnnotationWidth {
		width = maxAnnotationWidth
	}

	var expiresAt time.Time
	ttlMs := cmd.TTLMs
	if tool == AnnotationToolLaser && ttlMs <= 0 {
		ttlMs = 3000
	}
	if ttlMs > 0 {
		ttl := time.Duration(ttlMs) * time.Millisecond
		if ttl > MaxAnnotationTTL {
			ttl = MaxAnnotationTTL
		}
		expiresAt = now.Add(ttl)
	}

	return AnnotationShape{
		ID:        strings.TrimSpace(cmd.ID),
		Tool:      tool,
		ColorRGB:  parseHexRGB(cmd.Color, defaultAnnotationColor),
		Width:     width,
		Points:    pts,
		CreatedAt: now,
		ExpiresAt: expiresAt,
		Alpha:     1.0,
	}, nil
}

func clamp01(v float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func parseHexRGB(hex string, fallback uint32) uint32 {
	s := strings.TrimSpace(hex)
	s = strings.TrimPrefix(s, "#")
	if len(s) != 6 {
		return fallback
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return fallback
	}
	return uint32(v)
}

func downsamplePoints(pts []AnnotationPoint, limit int) []AnnotationPoint {
	if len(pts) <= limit || limit < 2 {
		out := make([]AnnotationPoint, len(pts))
		copy(out, pts)
		return out
	}
	out := make([]AnnotationPoint, 0, limit)
	lastIdx := len(pts) - 1
	for i := 0; i < limit-1; i++ {
		idx := (i * lastIdx) / (limit - 1)
		out = append(out, pts[idx])
	}
	out = append(out, pts[lastIdx])
	return out
}
