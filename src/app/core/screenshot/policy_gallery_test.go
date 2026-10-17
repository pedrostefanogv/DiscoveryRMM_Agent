package screenshot

import "testing"

func TestPolicyGalleryAndCopyDefaults(t *testing.T) {
	p := DefaultPolicy()
	if p.CopyOnConfirmEnabled() {
		t.Fatal("copyOnConfirm deve nascer DESLIGADO")
	}
	if !p.GalleryAllowed() {
		t.Fatal("galleryEnabled deve nascer LIGADO")
	}
	if p.GalleryItemLimit() != DefaultGalleryMaxItems {
		t.Fatalf("galleryMaxItems default = %d", p.GalleryItemLimit())
	}
	normalized := p.Normalize()
	if normalized.CopyOnConfirm == nil || normalized.GalleryEnabled == nil {
		t.Fatal("Normalize deve materializar os ponteiros dos novos campos")
	}
	if normalized.GalleryMaxItems != DefaultGalleryMaxItems || normalized.GalleryItemLimit() != DefaultGalleryMaxItems {
		t.Fatalf("galleryMaxItems apos Normalize = %d", normalized.GalleryMaxItems)
	}
}

func TestPolicyGalleryNormalizeClamps(t *testing.T) {
	cases := map[int]int{
		-5:                       DefaultGalleryMaxItems,
		0:                        DefaultGalleryMaxItems,
		1:                        1,
		50:                       50,
		MaxGalleryMaxItems:       MaxGalleryMaxItems,
		MaxGalleryMaxItems + 100: MaxGalleryMaxItems,
	}
	for in, want := range cases {
		p := Policy{GalleryMaxItems: in}
		if got := p.GalleryItemLimit(); got != want {
			t.Errorf("GalleryItemLimit(%d) = %d, want %d", in, got, want)
		}
		if got := p.Normalize().GalleryMaxItems; got != want {
			t.Errorf("Normalize(GalleryMaxItems=%d) = %d, want %d", in, got, want)
		}
	}
}

func TestPolicyGalleryAndCopyExplicitValues(t *testing.T) {
	copyOn := true
	galleryOff := false
	p := Policy{CopyOnConfirm: &copyOn, GalleryEnabled: &galleryOff, GalleryMaxItems: 10}
	n := p.Normalize()
	if !n.CopyOnConfirmEnabled() {
		t.Fatal("copyOnConfirm=true deveria sobreviver ao Normalize")
	}
	if n.GalleryAllowed() {
		t.Fatal("galleryEnabled=false deveria sobreviver ao Normalize")
	}
}
