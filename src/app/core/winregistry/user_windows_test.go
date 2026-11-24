//go:build windows

package winregistry

import (
	"testing"

	"golang.org/x/sys/windows/registry"
)

func TestResolveTarget_HKCU_InteractiveUser(t *testing.T) {
	origResolver := activeUserResolver
	t.Cleanup(func() { activeUserResolver = origResolver })

	fakeUser := &UserContext{
		SID:       "S-1-5-21-1000-2000-3000-1001",
		Username:  "pedro",
		Domain:    "WORKGROUP",
		SessionID: 1,
	}
	activeUserResolver = func() *UserContext { return fakeUser }

	// Raiz de HKCU -> HKEY_USERS\<SID>
	root, osPath, user, err := resolveTarget(HiveCurrentUser, "")
	if err != nil {
		t.Fatalf("resolveTarget raiz falhou: %v", err)
	}
	if root != registry.USERS {
		t.Errorf("root = %v; queria registry.USERS", root)
	}
	if osPath != fakeUser.SID {
		t.Errorf("osPath = %q; queria %q", osPath, fakeUser.SID)
	}
	if user == nil || user.Username != "pedro" {
		t.Errorf("user = %+v; queria pedro", user)
	}

	// Subchave de HKCU -> HKEY_USERS\<SID>\SOFTWARE\Test
	root, osPath, _, err = resolveTarget(HiveCurrentUser, "SOFTWARE\\Test")
	if err != nil {
		t.Fatalf("resolveTarget subchave falhou: %v", err)
	}
	if root != registry.USERS {
		t.Errorf("root = %v; queria registry.USERS", root)
	}
	want := fakeUser.SID + "\\SOFTWARE\\Test"
	if osPath != want {
		t.Errorf("osPath = %q; queria %q", osPath, want)
	}

	// HKLM nunca e redirecionado mesmo com usuario conectado.
	root, osPath, user, err = resolveTarget(HiveLocalMachine, "SOFTWARE\\Test")
	if err != nil {
		t.Fatalf("resolveTarget HKLM falhou: %v", err)
	}
	if root != registry.LOCAL_MACHINE {
		t.Errorf("HKLM root = %v; queria registry.LOCAL_MACHINE", root)
	}
	if osPath != "SOFTWARE\\Test" {
		t.Errorf("HKLM osPath = %q; queria SOFTWARE\\Test", osPath)
	}
	if user != nil {
		t.Errorf("HKLM user deveria ser nil, veio %+v", user)
	}
}

func TestResolveTarget_HKCU_FallbackWhenNoUser(t *testing.T) {
	origResolver := activeUserResolver
	t.Cleanup(func() { activeUserResolver = origResolver })

	activeUserResolver = func() *UserContext { return nil }

	root, osPath, user, err := resolveTarget(HiveCurrentUser, "SOFTWARE\\Test")
	if err != nil {
		t.Fatalf("resolveTarget fallback falhou: %v", err)
	}
	if root != registry.CURRENT_USER {
		t.Errorf("root = %v; queria registry.CURRENT_USER", root)
	}
	if osPath != "SOFTWARE\\Test" {
		t.Errorf("osPath = %q; queria SOFTWARE\\Test", osPath)
	}
	if user != nil {
		t.Errorf("user deveria ser nil no fallback, veio %+v", user)
	}
}

func TestListKey_IncludesWritableAndSkipsValuesOnPagination(t *testing.T) {
	if testing.Short() {
		t.Skip("leitura real no registro; pulado em -short")
	}

	page, err := ListKey(HiveLocalMachine, "SOFTWARE", "")
	if err != nil {
		t.Skipf("leitura de HKLM\\SOFTWARE falhou neste contexto: %v", err)
	}
	if page.Writable == nil || !*page.Writable {
		t.Errorf("HKLM\\SOFTWARE deveria retornar Writable=true em ListPage, veio %v", page.Writable)
	}

	// Pagina subsequente (offset > 0) nao precisa re-ler valores.
	page2, err := ListKey(HiveLocalMachine, "SOFTWARE", "1")
	if err != nil {
		t.Fatalf("ListKey com token falhou: %v", err)
	}
	if len(page2.Values) != 0 {
		t.Errorf("ListKey com offset > 0 deveria pular leitura de valores, veio %d", len(page2.Values))
	}
}
