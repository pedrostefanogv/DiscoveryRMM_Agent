//go:build windows

package tpmvault

// Implementação real do cofre no Windows: índice NV do TPM 2.0 + objeto
// KeyedHash selado.
//
// Receita validada no probe real (cmd/tpmvault-probe, WS0):
//
//   - abertura via tpm2/transport/windowstpm (TBS do go-tpm, mantido; valida
//     que o dispositivo é TPM 2.0 e devolve windowstpm.ErrNotTPM20 se não for);
//   - selagem sob a EK: CreatePrimary(RHEndorsement, RSAEKTemplate) SEM senha,
//     porém o tpm2.Create/Load exigem uma POLICY SESSION
//     PolicySecret(RH_ENDORSEMENT) — o EK tem userWithAuth=CLEAR/adminWithPolicy;
//   - objeto selado: KeyedHash/SHA256, atributos FixedTPM + FixedParent +
//     UserWithAuth + NoDA (SensitiveDataOrigin NÃO pode ser usado: o Create
//     devolve TPM_RC_ATTRIBUTES);
//   - auth do objeto selado = auth derivada do fingerprint;
//   - índice NV 0x0180D15C, DataSize 768, leitura/escrita em blocos de 512,
//     autorizadas PELO PRÓPRIO índice.
//
// Fora do SYSTEM (ou sem elevação) o TBS recusa NV_DefineSpace/NV_Write com
// TPM_E_COMMAND_BLOCKED; nesse caso Load/Save degradam graciosamente para
// ErrUnavailable e o erro exato fica em Status().LastError.
//
// Todas as operações são serializadas por mutex e cada sessão de TPM corre em
// goroutine com timeout de 10s; em timeout o handle é fechado para desbloquear a
// chamada pendente (sem goroutine presa).

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/go-tpm/tpm2"
	"github.com/google/go-tpm/tpm2/transport"
	// windowstpm é o transporte TBS MANTIDO do próprio go-tpm (o legado
	// tpmutil.OpenTPM está deprecado). Ele usa a mesma base
	// (transport.FromReadWriteCloser sobre tbs.dll) e ainda valida que o
	// dispositivo é TPM 2.0, devolvendo windowstpm.ErrNotTPM20 quando não é.
	"github.com/google/go-tpm/tpm2/transport/windowstpm"
)

const (
	// defaultNVIndex é o handle NV privado do Discovery (0x0180D15C).
	defaultNVIndex = 0x0180D15C
	// nvDataSize é o tamanho do índice NV, igual a envelopeMaxBytes.
	nvDataSize = envelopeMaxBytes
	// nvChunkSize é o maior bloco de NV_Read/NV_Write por comando.
	nvChunkSize = 512
	// tpmOperationTimeout limita cada sessão de TPM (abertura + comandos).
	tpmOperationTimeout = 10 * time.Second
	// authPrefix compõe a auth derivada do fingerprint.
	authPrefix = "discovery.tpmvault|v1|"
	// maxLastErrorBytes limita o LastError exposto no Status.
	maxLastErrorBytes = 512
)

// tokenLikeRE encontra valores com a cara do token do agente para redigi-los
// caso algum dia apareçam numa mensagem de erro.
var tokenLikeRE = regexp.MustCompile("mdz_[A-Za-z0-9._-]{2,}")

// platformVault é o cofre TPM 2.0 do Windows.
type platformVault struct {
	logf        func(string)
	fingerprint func() (ekHash, smbiosUUID string)
	index       uint32
	// enabled é sempre true aqui: Open(Enabled=false) devolve unavailableVault
	// (kill switch operacional). Mantido como defesa em profundidade caso
	// newPlatformVault seja chamado direto por um teste ou por outra rota.
	enabled bool

	mu      sync.Mutex
	probed  bool
	state   vaultState
	lastErr string
}

// vaultState é o retrato em memória do último estado conhecido do índice.
type vaultState struct {
	available     bool
	present       bool
	foreign       bool
	host          string
	tokenPrefix   string
	wroteAtUTC    string
	allowInsecure bool
}

// indexKind classifica a posse do índice NV.
type indexKind uint8

const (
	// indexUndefined: o índice não existe.
	indexUndefined indexKind = iota
	// indexOurs: índice com atributos nossos e (quando escrito) envelope DVLT.
	indexOurs
	// indexForeign: conteúdo de terceiros — nunca sobrescrever nem apagar.
	indexForeign
)

// indexInfo é o resultado da classificação do índice.
type indexInfo struct {
	kind indexKind
	pub  tpm2.TPMSNVPublic
	name tpm2.TPM2BName
	env  envelopeData
	// hasEnv indica que o envelope DVLT foi lido e decodificado com sucesso.
	hasEnv bool
	// corrupt indica índice NOSSO cujo envelope não decodificou (escrita
	// parcial, truncamento, digest inválido). É regravável pelo dono: nunca é
	// tratado como ErrForeign.
	corrupt bool
}

// newPlatformVault devolve o cofre TPM 2.0 real do Windows.
func newPlatformVault(opts Options) Vault {
	if !opts.Enabled {
		return newUnavailableVault(opts, "cofre TPM desabilitado por configuração")
	}
	index := opts.NVIndex
	if index == 0 {
		index = defaultNVIndex
	}
	return &platformVault{
		logf:        opts.Logf,
		fingerprint: opts.Fingerprint,
		index:       index,
		enabled:     true,
	}
}

// logfMsg escreve no Logf do chamador sem nunca incluir token.
func (v *platformVault) logfMsg(format string, args ...any) {
	if v.logf == nil {
		return
	}
	v.logf(sanitizeMessage(fmt.Sprintf(format, args...)))
}

// Status devolve o retrato mascarado do último estado conhecido. Na primeira
// chamada sonda o índice (NVReadPublic + leitura quando aplicável).
func (v *platformVault) Status() Status {
	v.mu.Lock()
	defer v.mu.Unlock()

	if !v.probed {
		rec, ok, err := v.loadLocked()
		v.applyLoad(rec, ok, err)
	}
	return Status{
		Enabled:          v.enabled,
		Available:        v.state.available,
		Present:          v.state.present,
		ForeignIndex:     v.state.foreign,
		Index:            v.index,
		Host:             v.state.host,
		TokenPrefix:      v.state.tokenPrefix,
		WroteAtUTC:       v.state.wroteAtUTC,
		AllowInsecureTLS: v.state.allowInsecure,
		LastError:        v.lastErr,
	}
}

// Load lê e dessela o registro do índice NV.
//
// Contrato: ErrUnavailable quando indisponível; (Record{}, false, nil) quando o
// índice não existe / ainda não foi escrito / está NV_UNINITIALIZED; ErrForeign
// quando o índice tem conteúdo de terceiros.
func (v *platformVault) Load() (Record, bool, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	rec, ok, err := v.loadLocked()
	v.applyLoad(rec, ok, err)
	return rec, ok, err
}

func (v *platformVault) loadLocked() (Record, bool, error) {
	v.probed = true
	if !v.enabled {
		return Record{}, false, ErrUnavailable
	}
	auth, err := v.deriveAuth()
	if err != nil {
		return Record{}, false, err
	}

	var (
		rec Record
		ok  bool
	)
	err = withTPM("ler registro do índice NV", func(t transport.TPM) error {
		info, err := classifyIndex(t, v.index, auth)
		if err != nil {
			return err
		}
		switch info.kind {
		case indexUndefined:
			return nil
		case indexForeign:
			return fmt.Errorf("%w: índice NV 0x%08X contém dados de terceiros", ErrForeign, v.index)
		}
		if !info.hasEnv {
			// Índice definido por nós, ainda sem registro escrito.
			return nil
		}
		payload, err := unsealPayload(t, auth, info.env.Private, info.env.Public)
		if err != nil {
			return fmt.Errorf("desselar registro do índice NV 0x%08X: %w", v.index, err)
		}
		parsed, err := ParseRecord(payload)
		if err != nil {
			return fmt.Errorf("%w: registro selado ilegível: %v", ErrForeign, err)
		}
		rec, ok = parsed, true
		return nil
	})
	if err != nil {
		return Record{}, false, normalizeUnavailable(err)
	}
	return rec, ok, nil
}

// Save sela e grava o registro no índice NV. É idempotente e nunca sobrescreve
// conteúdo de terceiros.
func (v *platformVault) Save(rec Record) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.probed = true

	if !v.enabled {
		return ErrUnavailable
	}

	payload, err := rec.Marshal()
	if err != nil {
		v.markErr(err)
		return err
	}
	auth, err := v.deriveAuth()
	if err != nil {
		v.markErr(err)
		return err
	}

	var unchanged bool
	err = withTPM("gravar registro no índice NV", func(t transport.TPM) error {
		info, err := classifyIndex(t, v.index, auth)
		if err != nil {
			return err
		}
		if info.kind == indexForeign {
			return fmt.Errorf("%w: índice NV 0x%08X contém dados de terceiros", ErrForeign, v.index)
		}
		if info.corrupt {
			v.logfMsg("tpmvault: envelope do índice NV 0x%08X está corrompido; regravando como dono", v.index)
		}

		// Idempotência barata: se já existe registro selado e o conteúdo
		// desselado é estruturalmente igual ao novo, não gastamos CreatePrimary
		// (RSA-2048) nem NVWrite. Se o unseal/parse falhar (corrupção),
		// seguimos para a regravação.
		if info.hasEnv {
			if existing, err := unsealPayload(t, auth, info.env.Private, info.env.Public); err == nil {
				if parsed, perr := ParseRecord(existing); perr == nil && parsed == rec {
					unchanged = true
					return nil
				}
			}
		}

		privateBlob, publicBlob, err := sealPayload(t, auth, payload)
		if err != nil {
			return err
		}
		envBytes, err := encodeEnvelope(envelopeData{
			Flags:      envelopeFlags(rec),
			PayloadLen: uint16(len(payload)),
			Private:    privateBlob,
			Public:     publicBlob,
		})
		if err != nil {
			return err
		}

		if info.kind == indexUndefined {
			return defineAndWrite(t, v.index, auth, envBytes)
		}

		if len(envBytes) > int(info.pub.DataSize) {
			// Política de redefinição: o índice é NOSSO — classifyIndex só chega
			// aqui com atributos/NameAlg/Policy iguais aos do cofre e posse
			// confirmada pela auth derivada do fingerprint no NVRead — e o
			// DataSize corrente não comporta o novo envelope, por exemplo um
			// índice criado por versão anterior com DataSize menor (ou
			// corrompido). Apagamos e redefinimos com nvDataSize. Conteúdo de
			// terceiros nunca chega aqui (já devolvemos ErrForeign).
			v.logfMsg("tpmvault: índice NV 0x%08X com DataSize=%d é menor que o envelope (%d bytes); redefinindo",
				v.index, info.pub.DataSize, len(envBytes))
			if err := nvUndefine(t, v.index); err != nil {
				return fmt.Errorf("NVUndefineSpace (redefinição do índice 0x%08X): %w", v.index, err)
			}
			return defineAndWrite(t, v.index, auth, envBytes)
		}

		if err := nvWriteAll(t, v.index, info.name, auth, envBytes); err != nil {
			return fmt.Errorf("NVWrite(0x%08X): %w", v.index, err)
		}
		return nil
	})
	if err != nil {
		err = normalizeUnavailable(err)
		v.markErr(err)
		return err
	}

	if unchanged {
		// Nada foi escrito: preserva WroteAtUTC do último Save real.
		v.state.available = true
		v.state.foreign = false
		v.state.present = true
		v.state.host = rec.Host()
		v.state.tokenPrefix = TokenPrefix(rec.Token())
		v.state.allowInsecure = rec.Insecure()
		v.lastErr = ""
		v.logfMsg("tpmvault: registro idêntico já presente no índice NV 0x%08X; nada a gravar", v.index)
		return nil
	}

	v.markSaved(rec)
	v.logfMsg("tpmvault: registro gravado no índice NV 0x%08X (host=%s, token=%s)",
		v.index, rec.Host(), TokenPrefix(rec.Token()))
	return nil
}

// Erase apaga o índice NV sob TPMRHOwner.
//
// Contrato: idempotente — nil quando o índice não existe (ou quando o cofre está
// desligado por configuração); ErrForeign quando o índice é de terceiros (não é
// apagado); ErrUnavailable quando o cofre está indisponível (sem fingerprint de
// hardware ou TPM/TBS inacessível). Nesse último caso o chamador NÃO pode
// assumir que o segredo foi removido.
func (v *platformVault) Erase() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.probed = true

	if !v.enabled {
		return nil
	}
	auth, err := v.deriveAuth()
	if err != nil {
		// Sem identidade estável não há como confirmar a posse do índice. Não
		// devolvemos nil: o descomissionamento remoto não pode achar que apagou.
		v.markErr(err)
		return err
	}

	err = withTPM("apagar índice NV", func(t transport.TPM) error {
		info, err := classifyIndex(t, v.index, auth)
		if err != nil {
			return err
		}
		switch info.kind {
		case indexUndefined:
			return nil
		case indexForeign:
			return fmt.Errorf("%w: índice NV 0x%08X contém dados de terceiros e não será apagado", ErrForeign, v.index)
		}
		if err := nvUndefine(t, v.index); err != nil {
			if errors.Is(err, tpm2.TPMRCHandle) {
				return nil
			}
			return fmt.Errorf("NVUndefineSpace(0x%08X): %w", v.index, err)
		}
		return nil
	})
	if err != nil {
		err = normalizeUnavailable(err)
		v.markErr(err)
		return err
	}

	v.markErased()
	v.logfMsg("tpmvault: índice NV 0x%08X apagado (ou já inexistente)", v.index)
	return nil
}

// applyLoad atualiza o retrato em memória a partir de um Load.
func (v *platformVault) applyLoad(rec Record, ok bool, err error) {
	if err != nil {
		v.markErr(err)
		return
	}
	v.state.available = true
	v.state.foreign = false
	v.state.present = ok
	v.state.host = ""
	v.state.tokenPrefix = ""
	v.state.allowInsecure = false
	if ok {
		v.state.host = rec.Host()
		v.state.tokenPrefix = TokenPrefix(rec.Token())
		v.state.allowInsecure = rec.Insecure()
	}
	v.lastErr = ""
}

// markSaved registra o retrato de uma gravação bem-sucedida.
func (v *platformVault) markSaved(rec Record) {
	v.state = vaultState{
		available:     true,
		present:       true,
		host:          rec.Host(),
		tokenPrefix:   TokenPrefix(rec.Token()),
		wroteAtUTC:    time.Now().UTC().Format(time.RFC3339),
		allowInsecure: rec.Insecure(),
	}
	v.lastErr = ""
}

// markErased registra o retrato de uma remoção bem-sucedida.
func (v *platformVault) markErased() {
	v.state = vaultState{available: true}
	v.lastErr = ""
}

// markErr registra a falha sanitizada e ajusta o retrato.
func (v *platformVault) markErr(err error) {
	v.probed = true
	v.lastErr = sanitizeMessage(err.Error())
	if errors.Is(err, ErrForeign) {
		v.state = vaultState{available: true, foreign: true}
		return
	}
	v.state = vaultState{}
}

// deriveAuth deriva a authValue do índice a partir do fingerprint de hardware:
// sha256("discovery.tpmvault|v1|" + fingerprint). Sem fingerprint estável o
// cofre é fail-closed (ErrUnavailable).
func (v *platformVault) deriveAuth() ([]byte, error) {
	ekHash, smbiosUUID := "", ""
	if v.fingerprint != nil {
		ekHash, smbiosUUID = v.fingerprint()
	}
	value := strings.TrimSpace(ekHash)
	if value == "" {
		value = strings.TrimSpace(smbiosUUID)
	}
	if value == "" {
		return nil, fmt.Errorf("%w: fingerprint de hardware vazio (nem EK nem SMBIOS)", ErrUnavailable)
	}
	sum := sha256.Sum256([]byte(authPrefix + value))
	return sum[:], nil
}

// normalizeUnavailable garante que falhas inesperadas de TPM/NV virem
// ErrUnavailable (degradação graciosa), preservando o texto exato para
// diagnóstico no LastError.
func normalizeUnavailable(err error) error {
	switch {
	case errors.Is(err, ErrUnavailable),
		errors.Is(err, ErrForeign),
		errors.Is(err, ErrInvalidValue),
		errors.Is(err, ErrNoRecord):
		return err
	default:
		if name := windowsTPMErrorName(err); name != "" {
			return fmt.Errorf("%w: %s: %w", ErrUnavailable, name, err)
		}
		return fmt.Errorf("%w: %s", ErrUnavailable, err.Error())
	}
}

// windowsTPMErrorNames nomeia os erros TBS/Windows (TPM_E_*) que o go-tpm
// devolve como "unrecognized error code 0x8028xxxx". Fonte: tbs.h do Windows.
var windowsTPMErrorNames = map[uint32]string{
	0x80280400: "TPM_E_COMMAND_BLOCKED",
	0x80280401: "TPM_E_INVALID_HANDLE",
	0x80280402: "TPM_E_DUPLICATE_HANDLE",
	0x80280403: "TPM_E_FAILED",
	0x80280404: "TPM_E_COMMAND_SIZE",
	0x80280405: "TPM_E_COMMAND_CODE",
	0x80280406: "TPM_E_AUTHSIZE",
	0x80280407: "TPM_E_AUTH_CONTEXT",
	0x80280408: "TPM_E_NV_RANGE",
	0x80280409: "TPM_E_NV_SIZE",
	0x8028040A: "TPM_E_NV_LOCKED",
	0x8028040B: "TPM_E_NV_AUTHORIZATION",
	0x8028040C: "TPM_E_NV_UNINITIALIZED",
	0x8028040D: "TPM_E_NV_SPACE",
	0x8028040E: "TPM_E_NV_DEFINED",
	0x8028040F: "TPM_E_BAD_CONTEXT",
	0x80280410: "TPM_E_CPHASH",
	0x80280411: "TPM_E_PARENT",
	0x80280412: "TPM_E_NEEDS_TEST",
	0x80280413: "TPM_E_NO_RESULT",
	0x80280414: "TPM_E_SENSITIVE",
}

// windowsTPMErrorName devolve "NOME (0xXXXXXXXX)" quando o TPMRC do erro é um
// código TPM_E_* do TBS; "" caso contrário.
func windowsTPMErrorName(err error) string {
	var rc tpm2.TPMRC
	if !errors.As(err, &rc) {
		return ""
	}
	name, ok := windowsTPMErrorNames[uint32(rc)]
	if !ok {
		return ""
	}
	return fmt.Sprintf("%s (0x%08X)", name, uint32(rc))
}

// sanitizeMessage remove qualquer coisa com cara de token e limita o tamanho.
func sanitizeMessage(msg string) string {
	msg = strings.TrimSpace(msg)
	msg = tokenLikeRE.ReplaceAllString(msg, "mdz_...")
	if len(msg) > maxLastErrorBytes {
		msg = msg[:maxLastErrorBytes] + "..."
	}
	return msg
}

// envelopeFlags monta o byte de flags do envelope.
func envelopeFlags(rec Record) byte {
	if rec.Insecure() {
		return envelopeFlagInsecureTLS
	}
	return 0
}

// -------- primitivas de TPM --------

// withTPM abre o TPM (TBS), executa fn e fecha o handle. Toda a sessão corre em
// goroutine com timeout de 10s; ao estourar, o handle é fechado para desbloquear
// a chamada pendente e a goroutine termina (canal com buffer).
func withTPM(op string, fn func(transport.TPM) error) error {
	done := make(chan error, 1)
	ready := make(chan io.Closer, 1)
	go func() {
		rwc, err := windowstpm.Open()
		if err != nil {
			done <- fmt.Errorf("abrir TPM via TBS: %w", err)
			return
		}
		ready <- rwc
		err = fn(rwc)
		_ = rwc.Close()
		done <- err
	}()

	var closer io.Closer
	timeout := time.NewTimer(tpmOperationTimeout)
	defer timeout.Stop()
	for {
		select {
		case err := <-done:
			return err
		case c := <-ready:
			closer = c
			ready = nil // não reagenda este caso
		case <-timeout.C:
			if closer != nil {
				_ = closer.Close()
			}
			return fmt.Errorf("timeout de %s na operação de TPM (%s)", tpmOperationTimeout, op)
		}
	}
}

// classifyIndex decide de quem é o índice NV e, quando é nosso e já escrito, lê e
// valida o envelope.
func classifyIndex(t transport.TPM, index uint32, auth []byte) (indexInfo, error) {
	pub, name, err := nvReadPublic(t, index)
	if err != nil {
		if errors.Is(err, tpm2.TPMRCHandle) {
			return indexInfo{kind: indexUndefined}, nil
		}
		return indexInfo{}, fmt.Errorf("NVReadPublic(0x%08X): %w", index, err)
	}

	info := indexInfo{kind: indexOurs, pub: pub, name: name}
	if !nvPublicMatchesOurs(pub) {
		info.kind = indexForeign
		return info, nil
	}
	if !pub.Attributes.Written {
		return info, nil
	}

	size := pub.DataSize
	if size > envelopeMaxBytes {
		size = envelopeMaxBytes
	}
	raw, err := nvReadAll(t, index, name, size, auth)
	if err != nil {
		switch {
		case errors.Is(err, tpm2.TPMRCNVUninitialized):
			return info, nil
		case isNVAuthorizationError(err):
			// Atributos iguais aos nossos, mas a auth do índice não confere:
			// foi gravado por outra identidade/máquina — tratar como terceiros.
			info.kind = indexForeign
			return info, nil
		default:
			return indexInfo{}, fmt.Errorf("NVRead(0x%08X): %w", index, err)
		}
	}

	env, err := decodeEnvelope(raw)
	if err != nil {
		// Discriminador de posse: chegamos aqui porque o NVRead foi autorizado
		// PELO PRÓPRIO índice com a auth derivada do fingerprint. Logo o índice
		// é NOSSO mesmo que o envelope esteja corrompido/truncado — uma única
		// gravação interrompida não pode inutilizar o cofre para sempre.
		// Exceção: envelope de versão FUTURA continua fail-closed (uma versão
		// antiga não rebaixa um registro mais novo).
		if errors.Is(err, errEnvelopeVersion) {
			info.kind = indexForeign
			return info, nil
		}
		info.corrupt = true
		return info, nil
	}
	info.env = env
	info.hasEnv = true
	return info, nil
}

// nvReadPublic lê a área pública do índice.
func nvReadPublic(t transport.TPM, index uint32) (tpm2.TPMSNVPublic, tpm2.TPM2BName, error) {
	rsp, err := tpm2.NVReadPublic{NVIndex: tpm2.TPMHandle(index)}.Execute(t)
	if err != nil {
		return tpm2.TPMSNVPublic{}, tpm2.TPM2BName{}, err
	}
	pub, err := rsp.NVPublic.Contents()
	if err != nil {
		return tpm2.TPMSNVPublic{}, tpm2.TPM2BName{}, fmt.Errorf("decodificar NVPublic: %w", err)
	}
	return *pub, rsp.NVName, nil
}

// nvReadAll lê size bytes do índice em blocos de 512, autorizando pelo próprio
// índice.
func nvReadAll(t transport.TPM, index uint32, name tpm2.TPM2BName, size uint16, auth []byte) ([]byte, error) {
	if size == 0 {
		return nil, nil
	}
	idxAuth := tpm2.AuthHandle{
		Handle: tpm2.TPMHandle(index),
		Name:   name,
		Auth:   tpm2.PasswordAuth(auth),
	}
	idxNamed := tpm2.NamedHandle{Handle: tpm2.TPMHandle(index), Name: name}

	out := make([]byte, 0, size)
	for off := uint16(0); off < size; off += nvChunkSize {
		n := size - off
		if n > nvChunkSize {
			n = nvChunkSize
		}
		rsp, err := tpm2.NVRead{
			AuthHandle: idxAuth,
			NVIndex:    idxNamed,
			Size:       n,
			Offset:     off,
		}.Execute(t)
		if err != nil {
			return nil, err
		}
		out = append(out, rsp.Data.Buffer...)
	}
	return out, nil
}

// nvWriteAll grava data no índice em blocos de 512, autorizando pelo próprio
// índice.
func nvWriteAll(t transport.TPM, index uint32, name tpm2.TPM2BName, auth, data []byte) error {
	idxAuth := tpm2.AuthHandle{
		Handle: tpm2.TPMHandle(index),
		Name:   name,
		Auth:   tpm2.PasswordAuth(auth),
	}
	idxNamed := tpm2.NamedHandle{Handle: tpm2.TPMHandle(index), Name: name}

	for off := 0; off < len(data); off += nvChunkSize {
		end := off + nvChunkSize
		if end > len(data) {
			end = len(data)
		}
		_, err := tpm2.NVWrite{
			AuthHandle: idxAuth,
			NVIndex:    idxNamed,
			Data:       tpm2.TPM2BMaxNVBuffer{Buffer: data[off:end]},
			Offset:     uint16(off),
		}.Execute(t)
		if err != nil {
			return err
		}
	}
	return nil
}

// nvDefine cria o índice NV sob o owner, com a auth derivada como authValue.
func nvDefine(t transport.TPM, index uint32, auth []byte) error {
	_, err := tpm2.NVDefineSpace{
		AuthHandle: tpm2.TPMRHOwner,
		Auth:       tpm2.TPM2BAuth{Buffer: auth},
		PublicInfo: tpm2.New2B(tpm2.TPMSNVPublic{
			NVIndex:    tpm2.TPMHandle(index),
			NameAlg:    tpm2.TPMAlgSHA256,
			Attributes: nvAttributes(),
			DataSize:   nvDataSize,
		}),
	}.Execute(t)
	return err
}

// nvUndefine remove o índice NV sob o owner.
func nvUndefine(t transport.TPM, index uint32) error {
	_, err := tpm2.NVUndefineSpace{
		AuthHandle: tpm2.TPMRHOwner,
		NVIndex:    tpm2.TPMHandle(index),
	}.Execute(t)
	return err
}

// defineAndWrite cria o índice e grava o envelope.
func defineAndWrite(t transport.TPM, index uint32, auth, data []byte) error {
	if err := nvDefine(t, index, auth); err != nil {
		return fmt.Errorf("NVDefineSpace(0x%08X): %w", index, err)
	}
	// Após o define o índice ainda não foi escrito; o Name muda quando
	// TPMA_NV_WRITTEN é setado, por isso lemos o Name corrente.
	pub, name, err := nvReadPublic(t, index)
	if err != nil {
		return fmt.Errorf("NVReadPublic pós-define(0x%08X): %w", index, err)
	}
	if !nvPublicMatchesOurs(pub) {
		return fmt.Errorf("%w: índice NV 0x%08X recém-definido não corresponde ao esperado", ErrForeign, index)
	}
	if err := nvWriteAll(t, index, name, auth, data); err != nil {
		return fmt.Errorf("NVWrite(0x%08X): %w", index, err)
	}
	return nil
}

// sealPayload sela payload sob a EK e devolve os blobs TPM2B já serializados.
func sealPayload(t transport.TPM, auth, payload []byte) (privateBlob, publicBlob []byte, err error) {
	prim, err := tpm2.CreatePrimary{
		PrimaryHandle: tpm2.TPMRHEndorsement,
		InPublic:      tpm2.New2B(tpm2.RSAEKTemplate),
	}.Execute(t)
	if err != nil {
		return nil, nil, fmt.Errorf("CreatePrimary(EK): %w", err)
	}
	defer flushContext(t, prim.ObjectHandle)

	// O EK tem userWithAuth=CLEAR e adminWithPolicy=true: Create/Load exigem uma
	// policy session que satisfaça PolicySecret(RH_ENDORSEMENT).
	parent := tpm2.AuthHandle{
		Handle: prim.ObjectHandle,
		Name:   prim.Name,
		Auth:   tpm2.Policy(tpm2.TPMAlgSHA256, 16, endorsementPolicy),
	}

	rsp, err := tpm2.Create{
		ParentHandle: parent,
		InSensitive: tpm2.TPM2BSensitiveCreate{
			Sensitive: &tpm2.TPMSSensitiveCreate{
				UserAuth: tpm2.TPM2BAuth{Buffer: auth},
				Data: tpm2.NewTPMUSensitiveCreate(&tpm2.TPM2BSensitiveData{
					Buffer: payload,
				}),
			},
		},
		InPublic: tpm2.New2B(tpm2.TPMTPublic{
			Type:    tpm2.TPMAlgKeyedHash,
			NameAlg: tpm2.TPMAlgSHA256,
			// SensitiveDataOrigin provocaria TPM_RC_ATTRIBUTES no Create.
			ObjectAttributes: tpm2.TPMAObject{
				FixedTPM:     true,
				FixedParent:  true,
				UserWithAuth: true,
				NoDA:         true,
			},
		}),
	}.Execute(t)
	if err != nil {
		return nil, nil, fmt.Errorf("Create(selagem): %w", err)
	}

	privateBlob, err = marshalSafe(rsp.OutPrivate)
	if err != nil {
		return nil, nil, err
	}
	publicBlob, err = marshalSafe(rsp.OutPublic)
	if err != nil {
		return nil, nil, err
	}
	return privateBlob, publicBlob, nil
}

// unsealPayload carrega o objeto selado sob a EK e o abre com a auth derivada.
func unsealPayload(t transport.TPM, auth, privateBlob, publicBlob []byte) ([]byte, error) {
	inPrivate, err := tpm2.Unmarshal[tpm2.TPM2BPrivate, *tpm2.TPM2BPrivate](privateBlob)
	if err != nil {
		return nil, fmt.Errorf("decodificar TPM2B_PRIVATE: %w", err)
	}
	inPublic, err := tpm2.Unmarshal[tpm2.TPM2BPublic, *tpm2.TPM2BPublic](publicBlob)
	if err != nil {
		return nil, fmt.Errorf("decodificar TPM2B_PUBLIC: %w", err)
	}

	prim, err := tpm2.CreatePrimary{
		PrimaryHandle: tpm2.TPMRHEndorsement,
		InPublic:      tpm2.New2B(tpm2.RSAEKTemplate),
	}.Execute(t)
	if err != nil {
		return nil, fmt.Errorf("CreatePrimary(EK): %w", err)
	}
	defer flushContext(t, prim.ObjectHandle)

	parent := tpm2.AuthHandle{
		Handle: prim.ObjectHandle,
		Name:   prim.Name,
		Auth:   tpm2.Policy(tpm2.TPMAlgSHA256, 16, endorsementPolicy),
	}

	obj, err := tpm2.Load{
		ParentHandle: parent,
		InPrivate:    *inPrivate,
		InPublic:     *inPublic,
	}.Execute(t)
	if err != nil {
		return nil, fmt.Errorf("Load(objeto selado): %w", err)
	}
	defer flushContext(t, obj.ObjectHandle)

	rsp, err := tpm2.Unseal{
		ItemHandle: tpm2.AuthHandle{
			Handle: obj.ObjectHandle,
			Name:   obj.Name,
			Auth:   tpm2.PasswordAuth(auth),
		},
	}.Execute(t)
	if err != nil {
		return nil, fmt.Errorf("Unseal: %w", err)
	}
	return append([]byte(nil), rsp.OutData.Buffer...), nil
}

// endorsementPolicy satisfaz a authPolicy do RSAEKTemplate com
// PolicySecret(RH_ENDORSEMENT).
func endorsementPolicy(t transport.TPM, handle tpm2.TPMISHPolicy, nonceTPM tpm2.TPM2BNonce) error {
	_, err := tpm2.PolicySecret{
		AuthHandle:    tpm2.TPMRHEndorsement,
		PolicySession: handle,
		NonceTPM:      nonceTPM,
	}.Execute(t)
	return err
}

// flushContext descarrega um handle transiente (melhor esforço).
func flushContext(t transport.TPM, handle tpm2.TPMHandle) {
	_, _ = tpm2.FlushContext{FlushHandle: handle}.Execute(t)
}

// marshalSafe serializa sem deixar um panic do tpm2.Marshal escapar.
func marshalSafe(v tpm2.Marshallable) (out []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			out = nil
			err = fmt.Errorf("falha ao serializar blob do TPM: %v", r)
		}
	}()
	return tpm2.Marshal(v), nil
}

// nvAttributes são os atributos do índice NV do cofre.
func nvAttributes() tpm2.TPMANV {
	return tpm2.TPMANV{
		NT:             tpm2.TPMNTOrdinary,
		AuthRead:       true,
		AuthWrite:      true,
		OwnerRead:      true,
		OwnerWrite:     true,
		NoDA:           true,
		PlatformCreate: false,
	}
}

// nvPublicMatchesOurs compara a área pública com o que o cofre define. O bit
// TPMA_NV_WRITTEN é ignorado de propósito (muda após a primeira escrita).
func nvPublicMatchesOurs(pub tpm2.TPMSNVPublic) bool {
	if pub.NameAlg != tpm2.TPMAlgSHA256 {
		return false
	}
	if pub.DataSize < envelopeMinBytes {
		return false
	}
	if len(pub.AuthPolicy.Buffer) != 0 {
		return false
	}
	return nvAttrsMatch(pub.Attributes, nvAttributes())
}

// nvAttrsMatch compara os atributos relevantes (ignora Written).
func nvAttrsMatch(got, want tpm2.TPMANV) bool {
	return got.PPWrite == want.PPWrite &&
		got.OwnerWrite == want.OwnerWrite &&
		got.AuthWrite == want.AuthWrite &&
		got.PolicyWrite == want.PolicyWrite &&
		got.NT == want.NT &&
		got.PolicyDelete == want.PolicyDelete &&
		got.WriteLocked == want.WriteLocked &&
		got.WriteAll == want.WriteAll &&
		got.WriteDefine == want.WriteDefine &&
		got.WriteSTClear == want.WriteSTClear &&
		got.GlobalLock == want.GlobalLock &&
		got.PPRead == want.PPRead &&
		got.OwnerRead == want.OwnerRead &&
		got.AuthRead == want.AuthRead &&
		got.PolicyRead == want.PolicyRead &&
		got.NoDA == want.NoDA &&
		got.Orderly == want.Orderly &&
		got.ClearSTClear == want.ClearSTClear &&
		got.ReadLocked == want.ReadLocked &&
		got.PlatformCreate == want.PlatformCreate &&
		got.ReadSTClear == want.ReadSTClear
}

// isNVAuthorizationError reconhece falhas de autorização do próprio índice.
func isNVAuthorizationError(err error) bool {
	for _, rc := range []tpm2.TPMRC{
		tpm2.TPMRCNVAuthorization,
		tpm2.TPMRCAuthFail,
		tpm2.TPMRCBadAuth,
		tpm2.TPMRCPolicyFail,
		tpm2.TPMRCAuthUnavailable,
	} {
		if errors.Is(err, rc) {
			return true
		}
	}
	return false
}
