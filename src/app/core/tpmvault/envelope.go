// Envelope binário gravado no índice NV do cofre TPM.
//
// Layout (big-endian, como o TPM 2.0 serializa inteiros):
//
//	offset  tamanho        campo
//	0       4              magic "DVLT"
//	4       1              versão do envelope (1)
//	5       1              flags (bit0 = AllowInsecureTLS)
//	6       2              payloadLen: bytes do JSON em claro antes da selagem
//	8       2              lenPrivate
//	10      lenPrivate     private: blob selado (TPM2B_PRIVATE marshalled)
//	...     2              lenPublic
//	...     lenPublic      public: blob selado (TPM2B_PUBLIC marshalled)
//	...     32             SHA-256 de tudo o que veio antes
//
// O envelope é a única parte do conteúdo do índice NV que NÃO está cifrada: por
// isso ele carrega um magic próprio e um digest. Qualquer conteúdo que não
// valide o magic/digest é tratado como de terceiros (ErrForeign) e jamais é
// sobrescrito.
//
// Este arquivo é puro (sem build tag) para poder ser testado em qualquer SO.
package tpmvault

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// errEnvelopeVersion marca especificamente "versão de envelope não suportada".
// O erro devolvido também satisfaz errors.Is(err, ErrForeign), mas permite ao
// chamador diferenciar um envelope de versão FUTURA (fail-closed: não é
// sobrescrito por uma versão antiga) de um envelope da versão corrente que
// apenas está corrompido/truncado (regravável pelo próprio dono).
var errEnvelopeVersion = errors.New("tpmvault: versão de envelope não suportada")

const (
	// envelopeMagic identifica o conteúdo como pertencente ao Discovery.
	envelopeMagic = "DVLT"
	// envelopeVersion é a versão corrente do formato do envelope.
	envelopeVersion byte = 1
	// envelopeFlagInsecureTLS marca servidor HTTP simples no registro.
	envelopeFlagInsecureTLS byte = 0x01

	// envelopeHeaderSize é magic+versão+flags+payloadLen.
	envelopeHeaderSize = 8
	// envelopeDigestSize é o tamanho do SHA-256 final.
	envelopeDigestSize = sha256.Size
	// envelopeMinBytes é o menor envelope possível (blobs vazios).
	envelopeMinBytes = envelopeHeaderSize + 2 + 2 + envelopeDigestSize
	// envelopeMaxBytes é o teto imposto pelo índice NV (DataSize de 768).
	envelopeMaxBytes = 768
)

// envelopeData é o conteúdo decodificado do envelope: os dois blobs do TPM e os
// metadados necessários para diagnóstico.
type envelopeData struct {
	// Flags replica envelopeFlagInsecureTLS.
	Flags byte
	// PayloadLen é o tamanho do JSON em claro que foi selado (informativo).
	PayloadLen uint16
	// Private é o TPM2B_PRIVATE serializado.
	Private []byte
	// Public é o TPM2B_PUBLIC serializado.
	Public []byte
}

// encodeEnvelope serializa o envelope e anexa o SHA-256 do corpo.
func encodeEnvelope(env envelopeData) ([]byte, error) {
	if len(env.Private) > math.MaxUint16 || len(env.Public) > math.MaxUint16 {
		return nil, fmt.Errorf("%w: blob selado excede %d bytes", ErrInvalidValue, math.MaxUint16)
	}
	total := envelopeHeaderSize + 2 + len(env.Private) + 2 + len(env.Public) + envelopeDigestSize
	if total > envelopeMaxBytes {
		return nil, fmt.Errorf("%w: envelope de %d bytes excede o índice NV de %d bytes",
			ErrInvalidValue, total, envelopeMaxBytes)
	}

	buf := make([]byte, 0, total)
	buf = append(buf, envelopeMagic...)
	buf = append(buf, envelopeVersion, env.Flags)
	buf = binary.BigEndian.AppendUint16(buf, env.PayloadLen)
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(env.Private)))
	buf = append(buf, env.Private...)
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(env.Public)))
	buf = append(buf, env.Public...)

	sum := sha256.Sum256(buf)
	buf = append(buf, sum[:]...)
	return buf, nil
}

// decodeEnvelope valida e decodifica um envelope lido do índice NV.
//
// Todo defeito estrutural (magic, versão, truncamento ou digest) devolve um erro
// que satisfaz errors.Is(err, ErrForeign). Uma versão de envelope FUTURA
// (raw[4] > envelopeVersion) devolve adicionalmente errEnvelopeVersion, para o
// chamador manter o índice fechado a downgrade; corrupção da versão corrente
// NÃO é marcada assim e pode ser regravada pelo dono (o discriminador de posse é
// a auth do índice derivada do fingerprint, verificada no NVRead).
func decodeEnvelope(raw []byte) (envelopeData, error) {
	if len(raw) < envelopeMinBytes {
		return envelopeData{}, fmt.Errorf("%w: envelope truncado (%d bytes, mínimo %d)",
			ErrForeign, len(raw), envelopeMinBytes)
	}
	if string(raw[0:4]) != envelopeMagic {
		return envelopeData{}, fmt.Errorf("%w: magic inesperado %q", ErrForeign, raw[0:4])
	}
	if raw[4] != envelopeVersion {
		return envelopeData{}, fmt.Errorf("%w: %w: versão %d", ErrForeign, errEnvelopeVersion, raw[4])
	}

	env := envelopeData{
		Flags:      raw[5],
		PayloadLen: binary.BigEndian.Uint16(raw[6:8]),
	}

	off := envelopeHeaderSize
	privLen := int(binary.BigEndian.Uint16(raw[off : off+2]))
	off += 2
	if off+privLen+2 > len(raw) {
		return envelopeData{}, fmt.Errorf("%w: lenPrivate=%d não cabe em %d bytes", ErrForeign, privLen, len(raw))
	}
	env.Private = append([]byte(nil), raw[off:off+privLen]...)
	off += privLen

	pubLen := int(binary.BigEndian.Uint16(raw[off : off+2]))
	off += 2
	if off+pubLen+envelopeDigestSize > len(raw) {
		return envelopeData{}, fmt.Errorf("%w: lenPublic=%d não cabe em %d bytes", ErrForeign, pubLen, len(raw))
	}
	env.Public = append([]byte(nil), raw[off:off+pubLen]...)
	off += pubLen

	sum := sha256.Sum256(raw[:off])
	if subtle.ConstantTimeCompare(sum[:], raw[off:off+envelopeDigestSize]) != 1 {
		return envelopeData{}, fmt.Errorf("%w: SHA-256 do envelope não confere", ErrForeign)
	}
	return env, nil
}
