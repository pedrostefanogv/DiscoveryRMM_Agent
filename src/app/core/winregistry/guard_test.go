package winregistry

import "testing"

// TestEvaluate_NeverAllowed verifica a REGRA 1: subarvores de credencial do SO
// recusadas inclusive para LEITURA.
//
// E a regra mais importante do guard: HKLM\SECURITY e HKLM\SAM guardam
// material de autenticacao. Se um teste futuro afrouxar isto, a feature deixa
// de ser segura — por isso o caso e explicito e nomeado.
func TestEvaluate_NeverAllowed(t *testing.T) {
	cases := []struct {
		name string
		hive Hive
		path string
	}{
		{"SAM raiz", HiveLocalMachine, "SAM"},
		{"SAM subchave", HiveLocalMachine, "SAM\\SAM\\Domains\\Account"},
		{"SECURITY raiz", HiveLocalMachine, "SECURITY"},
		{"SECURITY Policy", HiveLocalMachine, "SECURITY\\Policy\\Secrets"},
		{"LSA", HiveLocalMachine, "SYSTEM\\CurrentControlSet\\Control\\Lsa"},
		{"LSA Secrets", HiveLocalMachine, "SYSTEM\\CurrentControlSet\\Control\\Lsa\\Secrets"},
		{"SecurityProviders", HiveLocalMachine, "SYSTEM\\CurrentControlSet\\Control\\SecurityProviders"},
		{"Session Manager SAM", HiveLocalMachine, "SYSTEM\\CurrentControlSet\\Control\\Session Manager\\SAM"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, op := range []Op{OpRead, OpWrite, OpDelete} {
				got := Evaluate(tc.hive, tc.path, op)
				if got.Allowed {
					t.Fatalf("op=%s: esperava RECUSA em %s\\%s, veio ALLOW", op, tc.hive, tc.path)
				}
				if got.Rule == "" {
					t.Errorf("op=%s: recusa sem Rule; a auditoria precisa saber qual regra decidiu", op)
				}
			}
		})
	}
}

// TestEvaluate_CaseInsensitive garante que a comparacao nao e contornavel por
// caixa. O registro do Windows e case-insensitive: se o guard fosse sensivel a
// caixa, "security" passaria e a protecao seria decorativa.
func TestEvaluate_CaseInsensitive(t *testing.T) {
	paths := []string{
		"security",
		"Security",
		"SECURITY",
		"sam\\sam\\domains",
		"Sam\\Sam\\Domains",
		"system\\currentcontrolset\\control\\lsa",
	}
	for _, p := range paths {
		if got := Evaluate(HiveLocalMachine, p, OpRead); got.Allowed {
			t.Errorf("esperava recusa para %q (variacao de caixa), veio ALLOW", p)
		}
	}
}

// TestEvaluate_RunKeysReadOnly cobre a DECISAO DE PRODUTO confirmada com o
// usuario: Run/RunOnce sao leitura permitida e escrita bloqueada.
//
// O tecnico precisa ver o que inicia no boot (diagnostico de autostart e uma
// das razoes de existir um editor de registro em RMM); criar persistencia
// silenciosa e vetor classico de malware e nao e suporte.
func TestEvaluate_RunKeysReadOnly(t *testing.T) {
	runKeys := []string{
		"SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\Run",
		"SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\RunOnce",
		"SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\Run\\MinhaApp",
		"SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\RunOnce\\Instalador",
	}

	for _, p := range runKeys {
		if got := Evaluate(HiveLocalMachine, p, OpRead); !got.Allowed {
			t.Errorf("LEITURA de %q deve ser permitida (diagnostico), veio recusa: %s", p, got.Reason)
		}
		for _, op := range []Op{OpWrite, OpDelete} {
			if got := Evaluate(HiveLocalMachine, p, op); got.Allowed {
				t.Errorf("op=%s em %q deve ser RECUSADA (persistencia), veio ALLOW", op, p)
			}
		}
	}

	// A regra vale tambem em HKCU: e o mesmo vetor de persistencia por usuario.
	if got := Evaluate(HiveCurrentUser, "SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\Run", OpWrite); got.Allowed {
		t.Error("escrita em HKCU\\...\\Run deve ser recusada")
	}
}

// TestEvaluate_RunPrefixDoesNotMatchRunOnce e o caso que uma implementacao
// ingenua erra: "Run" e PREFIXO TEXTUAL de "RunOnce". Se a regra casasse por
// prefixo cru sem separador, a distincao entre as duas chaves sumiria.
//
// Aqui as duas DEVEM ser bloqueadas (ambas estao na lista), entao o teste
// verifica o inverso: que uma chave que apenas COMECA com "Run" nao e capturada
// por engano — ex.: "Runtime".
func TestEvaluate_SubtreeMatchBoundary(t *testing.T) {
	// "Runtime" NAO e subchave de "Run": nao deve casar com a regra de Run.
	if got := Evaluate(HiveLocalMachine, "SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\Runtime", OpWrite); !got.Allowed {
		t.Errorf("Runtime nao e subarvore de Run e nao deveria ser bloqueada; regra=%s", got.Rule)
	}
	// Mas "Run\\App" SIM e subarvore.
	if got := Evaluate(HiveLocalMachine, "SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\Run\\App", OpWrite); got.Allowed {
		t.Error("Run\\App e subarvore de Run e deve ser bloqueada")
	}
}

// TestEvaluate_SensitiveSuffixAnyHive cobre a REGRA 3: sufixos sensiveis
// bloqueados em QUALQUER hive.
//
// Casar por sufixo fecha as variacoes que uma lista de caminhos exatos deixaria
// passar — o SO procura IFEO em mais de um lugar e malware usa as variantes.
func TestEvaluate_SensitiveSuffixAnyHive(t *testing.T) {
	cases := []struct {
		hive Hive
		path string
	}{
		{HiveLocalMachine, "SOFTWARE\\Microsoft\\Windows NT\\CurrentVersion\\Image File Execution Options"},
		{HiveCurrentUser, "SOFTWARE\\Microsoft\\Windows NT\\CurrentVersion\\Image File Execution Options"},
		{HiveLocalMachine, "SOFTWARE\\Microsoft\\Windows Defender\\Exclusions\\Paths"},
		{HiveLocalMachine, "SOFTWARE\\Microsoft\\Windows Defender\\Exclusions\\Processes"},
		{HiveLocalMachine, "SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\Policies\\System"},
	}
	for _, tc := range cases {
		if got := Evaluate(tc.hive, tc.path, OpWrite); got.Allowed {
			t.Errorf("escrita em %s\\%s deveria ser recusada", tc.hive, tc.path)
		}
	}
}

// TestEvaluate_AgentOwnKeys garante que o agente nao pode ser sabotado por si
// mesmo: alterar a propria chave de desinstalacao quebraria o descomissionamento.
func TestEvaluate_AgentOwnKeys(t *testing.T) {
	p := "SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\Uninstall\\Discovery.RMM"
	if got := Evaluate(HiveLocalMachine, p, OpWrite); got.Allowed {
		t.Error("escrita na chave do proprio agente deve ser recusada")
	}
	// Leitura continua liberada: ajuda a diagnosticar instalacao quebrada.
	if got := Evaluate(HiveLocalMachine, p, OpRead); !got.Allowed {
		t.Errorf("leitura da chave do agente deve ser permitida; veio: %s", got.Reason)
	}
}

// TestEvaluate_AllowedPaths confirma que a postura fail-closed NAO virou
// "bloquear tudo": o uso normal precisa funcionar, senao a feature nao existe.
func TestEvaluate_AllowedPaths(t *testing.T) {
	cases := []struct {
		hive Hive
		path string
		op   Op
	}{
		{HiveLocalMachine, "", OpRead},
		{HiveLocalMachine, "SOFTWARE", OpRead},
		{HiveLocalMachine, "SOFTWARE\\MinhaEmpresa", OpWrite},
		{HiveLocalMachine, "SOFTWARE\\MinhaEmpresa\\App", OpDelete},
		{HiveCurrentUser, "SOFTWARE\\MinhaEmpresa\\Config", OpWrite},
		{HiveLocalMachine, "SYSTEM\\CurrentControlSet\\Services\\Spooler\\Parameters", OpRead},
		{HiveLocalMachine, "SOFTWARE\\Microsoft\\Windows NT\\CurrentVersion\\Winlogon", OpRead},
		{HiveClassesRoot, ".txt\\OpenWithProgids", OpWrite},
	}
	for _, tc := range cases {
		got := Evaluate(tc.hive, tc.path, tc.op)
		if !got.Allowed {
			t.Errorf("op=%s em %s\\%s deveria ser PERMITIDA; recusada por %s: %s",
				tc.op, tc.hive, tc.path, got.Rule, got.Reason)
		}
	}
}

// TestEvaluate_ServicesWriteBlocked garante que a arvore de servicos e
// somente-leitura aqui, apontando o operador para a aba Servicos (que tem guard
// proprio com contexto de controlador de dominio).
func TestEvaluate_ServicesWriteBlocked(t *testing.T) {
	p := "SYSTEM\\CurrentControlSet\\Services\\MinhaApp"
	if got := Evaluate(HiveLocalMachine, p, OpWrite); got.Allowed {
		t.Error("escrita em CurrentControlSet\\Services deve ser recusada (use a aba Servicos)")
	}
	if got := Evaluate(HiveLocalMachine, p, OpRead); !got.Allowed {
		t.Error("leitura de Services deve ser permitida")
	}
}

// TestEvaluate_RejectsInvalidInput confirma fail-closed em entradas invalidas.
func TestEvaluate_RejectsInvalidInput(t *testing.T) {
	// Hive fora da allowlist.
	if got := Evaluate(Hive("HKPD"), "QUALQUER", OpRead); got.Allowed {
		t.Error("hive desconhecido deve ser recusado")
	}
	if got := Evaluate(Hive(""), "", OpRead); got.Allowed {
		t.Error("hive vazio deve ser recusado")
	}
	// Operacao desconhecida.
	if got := Evaluate(HiveLocalMachine, "SOFTWARE", Op("execute")); got.Allowed {
		t.Error("operacao desconhecida deve ser recusada")
	}
	if got := Evaluate(HiveLocalMachine, "SOFTWARE", Op("")); got.Allowed {
		t.Error("operacao vazia deve ser recusada")
	}
	// Caminho com traversal.
	for _, p := range []string{"SOFTWARE\\..\\SYSTEM", "..", "SOFTWARE\\.\\SYSTEM"} {
		if got := Evaluate(HiveLocalMachine, p, OpRead); got.Allowed {
			t.Errorf("caminho %q deve ser recusado", p)
		}
	}
}

// TestOp_IsMutation documenta que delete conta como mutacao (exige confirmacao
// e, mais importante, nao tem desfazer).
func TestOp_IsMutation(t *testing.T) {
	if OpRead.IsMutation() {
		t.Error("leitura nao e mutacao")
	}
	if !OpWrite.IsMutation() {
		t.Error("escrita e mutacao")
	}
	if !OpDelete.IsMutation() {
		t.Error("exclusao e mutacao")
	}
}

// TestParseOp confirma que a normalizacao nao "adivinha".
func TestParseOp(t *testing.T) {
	for raw, want := range map[string]Op{
		"read": OpRead, "READ": OpRead, " Read ": OpRead,
		"write": OpWrite, "Write": OpWrite,
		"delete": OpDelete, "DELETE": OpDelete,
	} {
		got, ok := ParseOp(raw)
		if !ok || got != want {
			t.Errorf("ParseOp(%q) = (%v, %v); queria (%v, true)", raw, got, ok, want)
		}
	}
	for _, raw := range []string{"", "execute", "remove", "leitura"} {
		if _, ok := ParseOp(raw); ok {
			t.Errorf("ParseOp(%q) deveria devolver ok=false", raw)
		}
	}
}

// TestWritable espelha o que a UI usa para desabilitar botoes.
func TestWritable(t *testing.T) {
	if ok, reason := Writable(HiveLocalMachine, "SOFTWARE\\MinhaEmpresa"); !ok {
		t.Errorf("esperava escrita permitida; recusado: %s", reason)
	}
	ok, reason := Writable(HiveLocalMachine, "SAM")
	if ok {
		t.Error("SAM nao deveria ser gravavel")
	}
	if reason == "" {
		t.Error("recusa precisa de motivo para a UI explicar ao operador")
	}
}
