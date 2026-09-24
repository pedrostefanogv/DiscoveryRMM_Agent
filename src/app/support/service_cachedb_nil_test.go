package support

import (
	"testing"
	"time"
)

// fakeCacheDB implementa CacheDB para os testes de normalização de typed-nil.
type fakeCacheDB struct{}

func (f *fakeCacheDB) CacheGetJSON(string, any) (bool, error)        { return false, nil }
func (f *fakeCacheDB) CacheSetJSON(string, any, time.Duration) error { return nil }
func (f *fakeCacheDB) CacheDelete(string) error                      { return nil }

// Regressão: um (*database.DB)(nil) atribuído a CacheDB passava pela checagem
// "s.db != nil" mas falhava com "database indisponivel" em toda escrita de
// cache. A normalização deve transformá-lo em um CacheDB nulo.
func TestNormalizeCacheDBRejectsTypedNil(t *testing.T) {
	var nilDB *fakeCacheDB
	if got := normalizeCacheDB(nilDB); got != nil {
		t.Fatalf("normalizeCacheDB((*fakeCacheDB)(nil)) = %#v, esperado nil", got)
	}
	if got := normalizeCacheDB(nil); got != nil {
		t.Fatalf("normalizeCacheDB(nil) = %#v, esperado nil", got)
	}
	if got := normalizeCacheDB(&fakeCacheDB{}); got == nil {
		t.Fatal("normalizeCacheDB(&fakeCacheDB{}) = nil, esperado valor não nulo")
	}

	svc := NewService(Options{DB: nilDB})
	if svc.db != nil {
		t.Fatalf("NewService com DB tipado nulo expôs s.db != nil: %#v", svc.db)
	}
	svc.SetDB(nilDB)
	if svc.db != nil {
		t.Fatalf("SetDB com DB tipado nulo expôs s.db != nil: %#v", svc.db)
	}
	if svc.SetDB(&fakeCacheDB{}); svc.db == nil {
		t.Fatal("SetDB com DB real deixou s.db nulo")
	}
}
