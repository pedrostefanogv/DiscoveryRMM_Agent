// Package tpmvault persiste, no TPM 2.0 da máquina, o par mínimo
// (endereço da API + token do agente) que permite a um agente reinstalado numa
// máquina formatada voltar a ficar online sem novo deploy token.
//
// O payload em claro é SELADO por um objeto KeyedHash do TPM e guardado em um
// índice NV. Nada além do par URL+token (e a flag opcional de TLS inseguro) é
// persistido: agentId, client/site, NATS e políticas são puxados de novo da API.
package tpmvault

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Schema identifica a versão do formato do registro gravado no TPM.
const Schema = "discovery.tpmvault/v1"

// EntryKeyAPIURL é a chave lógica da única entrada do registro (ideia "API|URL").
const EntryKeyAPIURL = "API|URL"

// tokenPrefix é o prefixo canônico do authToken do agente.
const tokenPrefix = "mdz_"

var (
	// ErrUnavailable indica que o cofre não está disponível (sem TPM, TBS
	// inacessível, sem identidade de hardware para derivar a auth, ou desligado
	// por configuração).
	ErrUnavailable = errors.New("tpmvault: TPM indisponível")
	// ErrNoRecord indica que o índice existe mas ainda não foi escrito.
	ErrNoRecord = errors.New("tpmvault: nenhum registro no TPM")
	// ErrForeign indica que o índice NV existe com conteúdo de outra
	// aplicação/versão e NÃO deve ser sobrescrito.
	ErrForeign = errors.New("tpmvault: índice NV pertence a outra aplicação ou versão")
	// ErrInvalidValue indica valor de entrada inválido no formato token@host.
	ErrInvalidValue = errors.New("tpmvault: valor inválido")
)

// Record é o registro mínimo persistido (e selado) no TPM.
type Record struct {
	Schema string `json:"schema"`
	// APIURL é "<token>@<host>" com o scheme http:// prefixado apenas quando o
	// servidor usa HTTP simples; HTTPS é o default implícito.
	APIURL           string `json:"API|URL"`
	AllowInsecureTLS bool   `json:"a,omitempty"`
}

// Validate confere o registro antes de gravá-lo.
func (r Record) Validate() error {
	if strings.TrimSpace(r.Schema) != Schema {
		return fmt.Errorf("%w: schema inesperado %q", ErrInvalidValue, r.Schema)
	}
	if _, _, _, err := ParseValue(r.APIURL); err != nil {
		return err
	}
	return nil
}

// Marshal serializa o registro no JSON compacto do cofre.
func (r Record) Marshal() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(r)
}

// ParseRecord desserializa e valida o JSON do cofre.
func ParseRecord(data []byte) (Record, error) {
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return Record{}, fmt.Errorf("%w: json: %v", ErrInvalidValue, err)
	}
	if err := rec.Validate(); err != nil {
		return Record{}, err
	}
	return rec, nil
}

// Token devolve o token de comunicação do registro.
func (r Record) Token() string {
	token, _, _, err := ParseValue(r.APIURL)
	if err != nil {
		return ""
	}
	return token
}

// Host devolve o host da API do registro.
func (r Record) Host() string {
	_, host, _, err := ParseValue(r.APIURL)
	if err != nil {
		return ""
	}
	return host
}

// Insecure informa se o servidor do registro usa HTTP simples.
func (r Record) Insecure() bool {
	_, _, insecure, err := ParseValue(r.APIURL)
	if err != nil {
		return false
	}
	return insecure
}

// FormatValue monta "<token>@<host>", prefixando "http://" apenas quando o
// servidor usa HTTP simples (HTTPS fica implícito, para ocupar menos bytes).
func FormatValue(token, host string, insecure bool) (string, error) {
	token = strings.TrimSpace(token)
	if err := validateToken(token); err != nil {
		return "", err
	}
	host = normalizeHost(host)
	if err := validateHost(host); err != nil {
		return "", err
	}
	if insecure {
		return token + "@http://" + host, nil
	}
	return token + "@" + host, nil
}

// ParseValue desmonta "<token>@<host>" (com ou sem "http://") e devolve o
// token, o host e se o servidor usa HTTP simples.
func ParseValue(value string) (token, host string, insecure bool, err error) {
	value = strings.TrimSpace(value)
	at := strings.Index(value, "@")
	if at <= 0 || at == len(value)-1 {
		return "", "", false, fmt.Errorf("%w: formato esperado token@host", ErrInvalidValue)
	}
	token = strings.TrimSpace(value[:at])
	rest := strings.TrimSpace(value[at+1:])
	if strings.Contains(rest, "@") {
		return "", "", false, fmt.Errorf("%w: valor com mais de um @", ErrInvalidValue)
	}
	if err := validateToken(token); err != nil {
		return "", "", false, err
	}
	switch {
	case strings.HasPrefix(strings.ToLower(rest), "http://"):
		insecure = true
		host = rest[len("http://"):]
	case strings.HasPrefix(strings.ToLower(rest), "https://"):
		host = rest[len("https://"):]
	default:
		host = rest
	}
	host = normalizeHost(host)
	if err := validateHost(host); err != nil {
		return "", "", false, err
	}
	return token, host, insecure, nil
}

// TokenPrefix devolve o prefixo seguro para exibição/diagnóstico de um token.
func TokenPrefix(token string) string {
	token = strings.TrimSpace(token)
	if len(token) <= 12 {
		if len(token) <= 4 {
			return "mdz_..."
		}
		return token[:4] + "..."
	}
	return token[:12] + "..."
}

func validateToken(token string) error {
	if token == "" {
		return fmt.Errorf("%w: token ausente", ErrInvalidValue)
	}
	if strings.ContainsAny(token, " \t\r\n@") {
		return fmt.Errorf("%w: token contém separador inválido", ErrInvalidValue)
	}
	if !strings.HasPrefix(token, tokenPrefix) {
		return fmt.Errorf("%w: token sem prefixo %s", ErrInvalidValue, tokenPrefix)
	}
	if len(token) <= len(tokenPrefix) {
		return fmt.Errorf("%w: token truncado", ErrInvalidValue)
	}
	return nil
}

func normalizeHost(host string) string {
	host = strings.TrimSpace(host)
	host = strings.Trim(host, "/")
	return host
}

func validateHost(host string) error {
	if host == "" {
		return fmt.Errorf("%w: host ausente", ErrInvalidValue)
	}
	if strings.ContainsAny(host, " \t\r\n@") {
		return fmt.Errorf("%w: host com separador inválido", ErrInvalidValue)
	}
	if strings.Contains(host, "://") {
		return fmt.Errorf("%w: host com scheme indevido", ErrInvalidValue)
	}
	return nil
}

// Status é o retrato (mascarado) do cofre para diagnóstico/UI. Nunca contém o
// token completo.
type Status struct {
	// Enabled reflete a política efetiva vinda do servidor.
	Enabled bool `json:"enabled"`
	// Available indica que o TPM foi aberto e o índice pode ser lido/escrito.
	Available bool `json:"available"`
	// Present indica que existe registro selado válido no índice.
	Present bool `json:"present"`
	// ForeignIndex indica índice NV ocupado por conteúdo de terceiros.
	ForeignIndex bool `json:"foreignIndex,omitempty"`
	// Index é o handle NV usado (diagnóstico).
	Index uint32 `json:"index"`
	// Host é o host da API recuperado do registro (dado não secreto).
	Host string `json:"host,omitempty"`
	// TokenPrefix é apenas o prefixo do token (mdz_xxxx...), nunca o token.
	TokenPrefix string `json:"tokenPrefix,omitempty"`
	// WroteAtUTC é o instante da última gravação (se disponível).
	WroteAtUTC string `json:"wroteAtUtc,omitempty"`
	// AllowInsecureTLS replica a flag opcional do registro.
	AllowInsecureTLS bool `json:"allowInsecureTls,omitempty"`
	// LastError descreve a última falha (sanitizada, sem segredo).
	LastError string `json:"lastError,omitempty"`
}
