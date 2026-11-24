//go:build !windows

package screen

import (
	"image"
	"time"
)

// AnnotationOverlay é um stub no-op em plataformas não-Windows.
type AnnotationOverlay struct {
	store *AnnotationStore
}

// NewAnnotationOverlay cria um overlay stub em plataformas não-Windows.
func NewAnnotationOverlay(_ image.Rectangle) *AnnotationOverlay {
	return &AnnotationOverlay{store: NewAnnotationStore()}
}

// HandleCommand aplica o comando apenas no store em memória.
func (o *AnnotationOverlay) HandleCommand(raw []byte) error {
	if o == nil || o.store == nil {
		return nil
	}
	_, err := o.store.Apply(raw, time.Now())
	return err
}

// SetBounds limpa o store em plataformas não-Windows.
func (o *AnnotationOverlay) SetBounds(_ image.Rectangle) {
	if o == nil || o.store == nil {
		return
	}
	o.store.Clear()
}

// Clear limpa o store em plataformas não-Windows.
func (o *AnnotationOverlay) Clear() {
	if o == nil || o.store == nil {
		return
	}
	o.store.Clear()
}

// Close encerra o overlay stub.
func (o *AnnotationOverlay) Close() {
	o.Clear()
}
