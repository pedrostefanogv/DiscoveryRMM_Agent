package tpmvault

// Vault é o cofre de credenciais no TPM.
//
// Contrato (congelado):
//   - Load()  -> (Record{}, false, ErrUnavailable) quando o cofre não está
//     disponível; (Record{}, false, nil) quando está disponível mas ainda não há
//     registro; erro em falha inesperada de leitura/abertura.
//   - Save()  -> idempotente; nunca deve ser chamado a partir de dado lido do
//     próprio TPM (evita auto-alimentação).
//   - Erase() -> idempotente; nil quando não havia registro; ErrForeign quando
//     o índice pertence a terceiros (não é apagado); ErrUnavailable quando o
//     cofre está indisponível (o chamador não pode assumir que apagou).
//   - Todas as operações devem falhar de forma graciosa (o agente não pode
//     deixar de subir por causa do cofre) e jamais logar o token.
type Vault interface {
	Status() Status
	Load() (Record, bool, error)
	Save(Record) error
	Erase() error
}

// Options configura a abertura do cofre.
type Options struct {
	// Logf recebe linhas de log prefixadas pelo chamador (nil = descarta).
	Logf func(string)
	// Fingerprint devolve (hash da EK do TPM, UUID do SMBIOS). Usado para
	// derivar a auth do índice NV de forma estável entre formatações. Quando
	// ambos forem vazios o cofre fica indisponível (fail-closed).
	Fingerprint func() (ekHash, smbiosUUID string)
	// NVIndex sobrescreve o handle NV padrão (0 = usa o padrão da plataforma).
	NVIndex uint32
	// Enabled é o kill switch OPERACIONAL do cofre (ops/build). false desliga o
	// cofre por completo (Load/Save devolvem ErrUnavailable; Erase é no-op).
	//
	// ATENÇÃO: isto NÃO é a política do servidor. A política herdável
	// (tpmRecoveryEnabled) governa apenas GRAVAR e APAGAR, na camada de serviço:
	// a LEITURA de boot precisa funcionar antes de haver conexão (a política só é
	// conhecida depois que o agente sobe), por isso Open é chamado com
	// Enabled=true e a decisão de gravar/apagar fica no debug.Service.
	Enabled bool
}

// Open devolve o cofre para a plataforma atual.
func Open(opts Options) Vault {
	if opts.Logf == nil {
		opts.Logf = func(string) {}
	}
	return newPlatformVault(opts)
}

// unavailableVault implementa o cofre quando ele está desligado por política ou
// quando a plataforma não suporta TPM.
type unavailableVault struct {
	logf    func(string)
	enabled bool
	index   uint32
	reason  string
}

func newUnavailableVault(opts Options, reason string) Vault {
	return &unavailableVault{
		logf:    opts.Logf,
		enabled: opts.Enabled,
		index:   opts.NVIndex,
		reason:  reason,
	}
}

func (v *unavailableVault) Status() Status {
	return Status{
		Enabled:   v.enabled,
		Available: false,
		Present:   false,
		Index:     v.index,
		LastError: v.reason,
	}
}

func (v *unavailableVault) Load() (Record, bool, error) {
	return Record{}, false, ErrUnavailable
}

func (v *unavailableVault) Save(Record) error {
	return ErrUnavailable
}

func (v *unavailableVault) Erase() error {
	// Apagar um cofre indisponível é no-op com sucesso: o chamador não deve
	// falhar em descomissionamento por ausência de TPM.
	return nil
}
