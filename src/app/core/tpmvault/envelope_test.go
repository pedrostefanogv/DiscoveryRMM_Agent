package tpmvault

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"testing"
)

func sampleEnvelope() envelopeData {
	return envelopeData{
		Flags:      envelopeFlagInsecureTLS,
		PayloadLen: 96,
		Private:    bytes.Repeat([]byte{0xA5}, 117),
		Public:     bytes.Repeat([]byte{0x5A}, 50),
	}
}

func TestEnvelopeRoundTrip(t *testing.T) {
	env := sampleEnvelope()
	raw, err := encodeEnvelope(env)
	if err != nil {
		t.Fatalf("encodeEnvelope: %v", err)
	}
	if len(raw) != envelopeHeaderSize+2+len(env.Private)+2+len(env.Public)+envelopeDigestSize {
		t.Fatalf("tamanho do envelope = %d, inesperado", len(raw))
	}
	if string(raw[0:4]) != envelopeMagic || raw[4] != envelopeVersion {
		t.Fatalf("cabeçalho inesperado: %q v%d", raw[0:4], raw[4])
	}
	if raw[5] != envelopeFlagInsecureTLS {
		t.Fatalf("flags = %#x, quer %#x", raw[5], envelopeFlagInsecureTLS)
	}

	got, err := decodeEnvelope(raw)
	if err != nil {
		t.Fatalf("decodeEnvelope: %v", err)
	}
	if got.Flags != env.Flags || got.PayloadLen != env.PayloadLen {
		t.Fatalf("metadados divergentes: %+v vs %+v", got, env)
	}
	if !bytes.Equal(got.Private, env.Private) || !bytes.Equal(got.Public, env.Public) {
		t.Fatal("blobs divergentes no round-trip")
	}
}

func TestEnvelopeIgnoresTrailingBytes(t *testing.T) {
	env := sampleEnvelope()
	raw, err := encodeEnvelope(env)
	if err != nil {
		t.Fatalf("encodeEnvelope: %v", err)
	}
	// O índice NV tem 768 bytes fixos; o que sobra de uma gravação anterior
	// precisa ser ignorado.
	padded := append(append([]byte(nil), raw...), bytes.Repeat([]byte{0xFF}, nvDataSize-len(raw))...)

	got, err := decodeEnvelope(padded)
	if err != nil {
		t.Fatalf("decodeEnvelope com cauda: %v", err)
	}
	if !bytes.Equal(got.Private, env.Private) || !bytes.Equal(got.Public, env.Public) {
		t.Fatal("blobs divergentes com cauda")
	}
}

func TestEnvelopeRejectsWrongMagic(t *testing.T) {
	raw, err := encodeEnvelope(sampleEnvelope())
	if err != nil {
		t.Fatalf("encodeEnvelope: %v", err)
	}
	raw[0] = 'X'
	if _, err := decodeEnvelope(raw); !errors.Is(err, ErrForeign) {
		t.Fatalf("erro = %v, quer ErrForeign", err)
	}
}

func TestEnvelopeRejectsUnsupportedVersion(t *testing.T) {
	raw, err := encodeEnvelope(sampleEnvelope())
	if err != nil {
		t.Fatalf("encodeEnvelope: %v", err)
	}
	raw[4] = 2
	_, err = decodeEnvelope(raw)
	if !errors.Is(err, ErrForeign) {
		t.Fatalf("erro = %v, quer ErrForeign", err)
	}
	// Versão futura é distinguível de corrupção: o índice fica fail-closed.
	if !errors.Is(err, errEnvelopeVersion) {
		t.Fatalf("erro = %v, quer errEnvelopeVersion", err)
	}
}

func TestEnvelopeRejectsCorruptedDigest(t *testing.T) {
	raw, err := encodeEnvelope(sampleEnvelope())
	if err != nil {
		t.Fatalf("encodeEnvelope: %v", err)
	}
	// Corrompe o último byte do SHA-256.
	raw[len(raw)-1] ^= 0xFF
	_, err = decodeEnvelope(raw)
	if !errors.Is(err, ErrForeign) {
		t.Fatalf("erro = %v, quer ErrForeign", err)
	}
	// Corrupção NÃO é versão futura: o dono pode regravar.
	if errors.Is(err, errEnvelopeVersion) {
		t.Fatalf("corrupção de digest marcada como versão futura: %v", err)
	}

	// Corrompe o conteúdo e mantém o digest: também precisa falhar.
	raw2, err := encodeEnvelope(sampleEnvelope())
	if err != nil {
		t.Fatalf("encodeEnvelope: %v", err)
	}
	raw2[envelopeHeaderSize+2] ^= 0x01
	if _, err := decodeEnvelope(raw2); !errors.Is(err, ErrForeign) {
		t.Fatalf("erro = %v, quer ErrForeign", err)
	}
}

func TestEnvelopeRejectsTruncation(t *testing.T) {
	raw, err := encodeEnvelope(sampleEnvelope())
	if err != nil {
		t.Fatalf("encodeEnvelope: %v", err)
	}
	for _, cut := range []int{0, 4, envelopeHeaderSize, envelopeHeaderSize + 2, len(raw) - envelopeDigestSize, len(raw) - 1} {
		if cut < 0 || cut >= len(raw) {
			continue
		}
		if _, err := decodeEnvelope(raw[:cut]); !errors.Is(err, ErrForeign) {
			t.Fatalf("corte em %d: erro = %v, quer ErrForeign", cut, err)
		}
	}

	// lenPrivate mentiroso (maior do que o buffer).
	bad := append([]byte(nil), raw...)
	bad[envelopeHeaderSize] = 0xFF
	bad[envelopeHeaderSize+1] = 0xFF
	if _, err := decodeEnvelope(bad); !errors.Is(err, ErrForeign) {
		t.Fatalf("lenPrivate mentiroso: erro = %v, quer ErrForeign", err)
	}

	// lenPublic mentiroso.
	bad2 := append([]byte(nil), raw...)
	off := envelopeHeaderSize + 2 + len(sampleEnvelope().Private)
	bad2[off] = 0xFF
	bad2[off+1] = 0xFF
	if _, err := decodeEnvelope(bad2); !errors.Is(err, ErrForeign) {
		t.Fatalf("lenPublic mentiroso: erro = %v, quer ErrForeign", err)
	}
}

func TestEnvelopeRejectsOversize(t *testing.T) {
	env := envelopeData{Private: bytes.Repeat([]byte{0x01}, 700), Public: bytes.Repeat([]byte{0x02}, 100)}
	if _, err := encodeEnvelope(env); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("erro = %v, quer ErrInvalidValue", err)
	}

	env2 := envelopeData{Private: bytes.Repeat([]byte{0x01}, envelopeMaxBytes)}
	if _, err := encodeEnvelope(env2); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("erro = %v, quer ErrInvalidValue", err)
	}
}

func TestEnvelopeDigestCoversEverything(t *testing.T) {
	env := sampleEnvelope()
	raw, err := encodeEnvelope(env)
	if err != nil {
		t.Fatalf("encodeEnvelope: %v", err)
	}
	body := raw[:len(raw)-envelopeDigestSize]
	sum := sha256.Sum256(body)
	if !bytes.Equal(sum[:], raw[len(raw)-envelopeDigestSize:]) {
		t.Fatal("SHA-256 do envelope não cobre o corpo inteiro")
	}
}
