package winregistry

import "testing"

// TestParseHive cobre a normalizacao e a recusa explicita.
func TestParseHive(t *testing.T) {
	ok := map[string]Hive{
		"HKLM": HiveLocalMachine, "hklm": HiveLocalMachine, "Hklm": HiveLocalMachine,
		"HKEY_LOCAL_MACHINE": HiveLocalMachine, "hkey_local_machine": HiveLocalMachine,
		"HKCU": HiveCurrentUser, "HKEY_CURRENT_USER": HiveCurrentUser,
		"HKCR": HiveClassesRoot, "HKEY_CLASSES_ROOT": HiveClassesRoot,
		"HKU": HiveUsers, "HKEY_USERS": HiveUsers,
		"HKCC": HiveCurrentConfig, "HKEY_CURRENT_CONFIG": HiveCurrentConfig,
		" HKLM ": HiveLocalMachine,
	}
	for raw, want := range ok {
		got, valid := ParseHive(raw)
		if !valid || got != want {
			t.Errorf("ParseHive(%q) = (%v, %v); queria (%v, true)", raw, got, valid, want)
		}
	}

	// Recusas explicitas. HKEY_PERFORMANCE_DATA e HKEY_DYN_DATA existem na
	// documentacao mas NAO sao enderecaveis como caminho textual: recusa-los
	// com motivo e melhor que cai-los no "desconhecido" generico.
	bad := []string{
		"", "   ", "HKPD", "HKEY_PERFORMANCE_DATA", "HKEY_DYN_DATA",
		"HKLM\\SOFTWARE", "foo", "HKEY_",
	}
	for _, raw := range bad {
		if got, valid := ParseHive(raw); valid {
			t.Errorf("ParseHive(%q) = (%v, true); deveria recusar", raw, got)
		}
	}
}

// TestCanonicalHives garante que a ordem de exibicao nao muda em silencio.
func TestCanonicalHives(t *testing.T) {
	hives := CanonicalHives()
	if len(hives) != 5 {
		t.Fatalf("esperava 5 hives, veio %d", len(hives))
	}
	if hives[0] != HiveLocalMachine || hives[1] != HiveCurrentUser {
		t.Errorf("ordem inesperada: %v", hives)
	}
	// Todos devem ser parseaveis de volta (round-trip).
	for _, h := range hives {
		got, ok := ParseHive(string(h))
		if !ok || got != h {
			t.Errorf("round-trip falhou para %v", h)
		}
	}
}

// TestHive_IsMachineHive alimenta a politica de consentimento por hive.
func TestHive_IsMachineHive(t *testing.T) {
	machine := []Hive{HiveLocalMachine, HiveClassesRoot, HiveCurrentConfig}
	for _, h := range machine {
		if !h.IsMachineHive() {
			t.Errorf("%v deveria ser hive de maquina", h)
		}
	}
	user := []Hive{HiveCurrentUser, HiveUsers}
	for _, h := range user {
		if h.IsMachineHive() {
			t.Errorf("%v NAO deveria ser hive de maquina", h)
		}
	}
}

// TestNormalizePath cobre a validacao de caminho.
func TestNormalizePath(t *testing.T) {
	ok := map[string]string{
		"":                              "",
		"SOFTWARE":                      "SOFTWARE",
		"SOFTWARE\\Microsoft":           "SOFTWARE\\Microsoft",
		"\\SOFTWARE\\Microsoft\\":       "SOFTWARE\\Microsoft",
		"SOFTWARE/Microsoft":            "SOFTWARE\\Microsoft",
		"  SOFTWARE\\Test  ":            "SOFTWARE\\Test",
		"SOFTWARE\\Classes\\.txt":       "SOFTWARE\\Classes\\.txt",
		"SOFTWARE\\App\\Sub\\Deep\\Key": "SOFTWARE\\App\\Sub\\Deep\\Key",
	}
	for raw, want := range ok {
		got, err := NormalizePath(raw)
		if err != nil {
			t.Errorf("NormalizePath(%q) erro inesperado: %v", raw, err)
			continue
		}
		if got != want {
			t.Errorf("NormalizePath(%q) = %q; queria %q", raw, got, want)
		}
	}

	bad := []string{
		"SOFTWARE\\..\\SYSTEM",
		"..",
		"SOFTWARE\\..",
		"../SOFTWARE",
		"SOFTWARE\\.\\Test",
		".",
		"SOFTWARE\\Test\x00",
		"SOFTWARE\\Test\n",
	}
	for _, raw := range bad {
		if got, err := NormalizePath(raw); err == nil {
			t.Errorf("NormalizePath(%q) = %q, nil; deveria falhar", raw, got)
		}
	}
}

// TestNormalizePath_TooLong: recusar com erro claro em vez de truncar. Truncar
// gravaria na chave errada silenciosamente — o pior desfecho possivel.
func TestNormalizePath_TooLong(t *testing.T) {
	long := ""
	for i := 0; i < MaxPathLen; i++ {
		long += "A"
	}
	if _, err := NormalizePath(long); err != nil {
		t.Errorf("caminho exatamente no limite deveria passar: %v", err)
	}
	if _, err := NormalizePath(long + "A"); err != ErrInvalidPath {
		t.Errorf("caminho acima do limite err = %v; queria ErrInvalidPath", err)
	}
}

// TestValidateValueName: nome VAZIO e valido e significa "(Default)".
//
// Tratar vazio como erro quebraria a leitura da maioria das chaves do Windows
// — praticamente toda chave tem um valor (Default).
func TestValidateValueName(t *testing.T) {
	valid := []string{"", "Nome", "Nome Com Espaco", "Nome-Com-Hifen", "Nome.Com.Ponto", "1"}
	for _, n := range valid {
		if err := ValidateValueName(n); err != nil {
			t.Errorf("ValidateValueName(%q) = %v; queria nil", n, err)
		}
	}
	if err := ValidateValueName("com\x00nul"); err == nil {
		t.Error("nome com NUL deveria falhar")
	}
	long := ""
	for i := 0; i <= MaxValueNameLen; i++ {
		long += "A"
	}
	if err := ValidateValueName(long); err == nil {
		t.Error("nome acima do limite deveria falhar")
	}
}

// TestSplitFullPath cobre o caminho completo no formato que o usuario le.
func TestSplitFullPath(t *testing.T) {
	hive, path, err := SplitFullPath("HKLM\\SOFTWARE\\Microsoft")
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if hive != HiveLocalMachine || path != "SOFTWARE\\Microsoft" {
		t.Errorf("SplitFullPath = (%v, %q); queria (HKLM, SOFTWARE\\Microsoft)", hive, path)
	}

	// So o hive.
	hive, path, err = SplitFullPath("HKCU")
	if err != nil || hive != HiveCurrentUser || path != "" {
		t.Errorf("SplitFullPath(HKCU) = (%v, %q, %v)", hive, path, err)
	}

	// Hive invalido.
	if _, _, err := SplitFullPath("HKPD\\SOFTWARE"); err != ErrInvalidHive {
		t.Errorf("hive invalido err = %v; queria ErrInvalidHive", err)
	}
	// Caminho invalido.
	if _, _, err := SplitFullPath("HKLM\\SOFTWARE\\..\\SYSTEM"); err != ErrInvalidPath {
		t.Errorf("caminho invalido err = %v; queria ErrInvalidPath", err)
	}
	// Vazio.
	if _, _, err := SplitFullPath(""); err != ErrInvalidHive {
		t.Errorf("vazio err = %v; queria ErrInvalidHive", err)
	}
}

// TestFullPath: round-trip com SplitFullPath.
func TestFullPath(t *testing.T) {
	if got := FullPath(HiveLocalMachine, "SOFTWARE\\Foo"); got != "HKLM\\SOFTWARE\\Foo" {
		t.Errorf("FullPath = %q", got)
	}
	if got := FullPath(HiveCurrentUser, ""); got != "HKCU" {
		t.Errorf("FullPath raiz = %q; queria HKCU", got)
	}
	// Round-trip.
	full := FullPath(HiveLocalMachine, "SOFTWARE\\Foo\\Bar")
	hive, path, err := SplitFullPath(full)
	if err != nil || hive != HiveLocalMachine || path != "SOFTWARE\\Foo\\Bar" {
		t.Errorf("round-trip de %q = (%v, %q, %v)", full, hive, path, err)
	}
}
