package winregistry

import "errors"

// Erros sentinela. O chamador (session_registry.go) traduz cada um para uma
// mensagem estavel no protocolo — a UI decide o que fazer pelo codigo, nao pelo
// texto, e os testes asseveram o sentinela em vez da string.
var (
	// ErrUnsupportedPlatform e devolvido pelos stubs nao-Windows.
	ErrUnsupportedPlatform = errors.New("winregistry: operacao suportada apenas no Windows")
	// ErrInvalidHive: hive fora da allowlist. Nunca "adivinha".
	ErrInvalidHive = errors.New("winregistry: hive invalido ou nao permitido")
	// ErrInvalidPath: path vazio, com .., absoluto, NUL ou acima do limite.
	ErrInvalidPath = errors.New("winregistry: caminho invalido")
	// ErrProtectedKey: o GUARD recusou a operacao nesta chave, por politica.
	// O motivo da regra vem anexado (ver GuardDecision.Reason).
	ErrProtectedKey = errors.New("winregistry: chave protegida por politica")
	// ErrAccessDenied: o SO recusou por ACL do registro.
	//
	// Distinto de ErrProtectedKey DE PROPOSITO: "bloqueado por nossa politica" e
	// "sem privilegio para esta chave" sao diagnosticos diferentes. Misturar os
	// dois faz o operador achar que a chave esta na denylist quando na verdade
	// ele so precisa de elevacao (ou a chave e de outro usuario) — e polui a
	// auditoria, que passa a registrar "politica" para um erro de permissao.
	ErrAccessDenied = errors.New("winregistry: acesso negado pela ACL do registro")
	// ErrKeyNotFound / ErrValueNotFound: distinguem "nao existe" de "ACL negou".
	// Sem isso o operador nao sabe se errou o caminho ou se falta privilegio.
	ErrKeyNotFound   = errors.New("winregistry: chave nao encontrada")
	ErrValueNotFound = errors.New("winregistry: valor nao encontrado")
	// ErrUnsupportedType: tipo de valor fora da allowlist.
	ErrUnsupportedType = errors.New("winregistry: tipo de valor nao suportado")
	// ErrInvalidData: dado incompativel com o tipo declarado (ex.: DWORD nao numerico).
	ErrInvalidData = errors.New("winregistry: dado invalido para o tipo informado")
	// ErrConfirmationRequired: mutacao sem confirmed=true. Ultima barreira do
	// agente caso a UI/API falhem em exigir a confirmacao.
	ErrConfirmationRequired = errors.New("winregistry: operacao de escrita exige confirmacao explicita")
	// ErrKeyNotEmpty: exclusao nao-recursiva de uma chave com filhos/valores.
	// Erro acionavel em vez do ERROR_ACCESS_DENIED cru do SO.
	ErrKeyNotEmpty = errors.New("winregistry: a chave nao esta vazia")
	// ErrLimitExceeded: resultado grande demais para uma resposta (ex.: export).
	ErrLimitExceeded = errors.New("winregistry: limite de tamanho excedido")
)

// ValueType e o tipo de um valor do registro, na forma canonica do Windows
// ("REG_SZ", "REG_DWORD", ...). String estavel no protocolo: nunca serializar
// o uint32 cru, que muda de significado entre versoes do header.
type ValueType string

const (
	TypeString                   ValueType = "REG_SZ"
	TypeExpandString             ValueType = "REG_EXPAND_SZ"
	TypeDWORD                    ValueType = "REG_DWORD"
	TypeQWORD                    ValueType = "REG_QWORD"
	TypeMultiString              ValueType = "REG_MULTI_SZ"
	TypeBinary                   ValueType = "REG_BINARY"
	TypeNone                     ValueType = "REG_NONE"
	TypeDWORDBigEndian           ValueType = "REG_DWORD_BIG_ENDIAN"
	TypeLink                     ValueType = "REG_LINK"
	TypeResourceList             ValueType = "REG_RESOURCE_LIST"
	TypeFullResourceDescriptor   ValueType = "REG_FULL_RESOURCE_DESCRIPTOR"
	TypeResourceRequirementsList ValueType = "REG_RESOURCE_REQUIREMENTS_LIST"
)

// ValueEntry e um valor do registro ja decodificado para o protocolo.
//
// Data e sempre string, inclusive para tipos binarios (base64). Um unico tipo
// de campo evita que o JSON do protocolo carregue numeros de 64 bits, que o
// JavaScript do console perde em silencio acima de 2^53 — o bug classico de
// editores de registro via web.
type ValueEntry struct {
	Name string    `json:"name"`
	Type ValueType `json:"type"`
	Data string    `json:"data"`
	// StringList so e preenchido para REG_MULTI_SZ. Mantido em paralelo a Data
	// para a UI nao precisar re-parsear (e para preservar string vazia no meio,
	// que um join/split perderia).
	StringList []string `json:"stringList,omitempty"`
	// ReadError marca um valor que EXISTE mas nao pode ser lido (ACL por valor,
	// tipo ilegivel, dado corrompido).
	//
	// Sem este campo, uma leitura falha era indistinguivel de um REG_NONE que
	// realmente tem dado vazio: o operador via "REG_NONE / vazio" e concluia que
	// o valor estava limpo, quando na verdade o agente nao conseguiu le-lo.
	ReadError string `json:"readError,omitempty"`
}

// UserContext descreve o usuario interativo conectado cuja arvore HKCU esta
// sendo exibida via HKEY_USERS\<SID>.
type UserContext struct {
	SID       string `json:"sid"`
	Username  string `json:"username"`
	Domain    string `json:"domain,omitempty"`
	SessionID uint32 `json:"sessionId"`
}

// DisplayName devolve "DOMAIN\\user" quando Domain esta preenchido, ou apenas Username.
func (u *UserContext) DisplayName() string {
	if u == nil {
		return ""
	}
	if u.Domain != "" {
		return u.Domain + "\\" + u.Username
	}
	return u.Username
}

// ListPage e o resultado paginado de um listKey.
//
// HasMore + NextToken existem porque HKLM\SOFTWARE\Classes tem dezenas de
// milhares de subchaves: carregar tudo numa resposta estoura o limite do NATS e
// trava a UI. Mesmo padrao de paginacao ja usado no navegador de arquivos.
type ListPage struct {
	Hive    string       `json:"hive"`
	Path    string       `json:"path"`
	Subkeys []string     `json:"subkeys"`
	Values  []ValueEntry `json:"values"`
	HasMore bool         `json:"hasMore"`
	// NextToken e opaco para o cliente: o agente decide o formato.
	NextToken string `json:"nextToken,omitempty"`
	// SubkeyCount/ValueCount sao os totais reais, para a UI mostrar "200 de 8.431".
	SubkeyCount int `json:"subkeyCount"`
	ValueCount  int `json:"valueCount"`
	// Writable e DenyReason permitem que a UI saiba a politica da chave sem
	// precisar disparar um statKey paralelo a cada listKey.
	Writable   *bool        `json:"writable,omitempty"`
	DenyReason string       `json:"denyReason,omitempty"`
	ActiveUser *UserContext `json:"-"`
}

// KeyInfo e um no da arvore, devolvido pela navegacao (sem valores).
type KeyInfo struct {
	Hive        string `json:"hive"`
	Path        string `json:"path"`
	SubkeyCount int    `json:"subkeyCount"`
	ValueCount  int    `json:"valueCount"`
	// Writable indica se o guard permite MUTACAO aqui. A UI usa isto para
	// desabilitar os botoes de escrita ANTES de o usuario tentar — evita a
	// experiencia de clicar e receber erro.
	Writable bool `json:"writable"`
	// DenyReason explica por que Writable=false (regra do guard).
	DenyReason string       `json:"denyReason,omitempty"`
	ActiveUser *UserContext `json:"-"`
}
