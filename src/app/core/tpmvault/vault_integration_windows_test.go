//go:build windows

package tpmvault

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/google/go-tpm/tpm2"
	"github.com/google/go-tpm/tpm2/transport"
)

// TestVaultIntegrationWindows executa o round-trip REAL no TPM da máquina:
// Save -> Load -> compara -> Erase -> Load confirma ausência.
//
// Gated por DISCOVERY_TPM_TEST=1. Fora de SYSTEM/elevação o TBS recusa
// NV_DefineSpace/NV_Write (TPM_E_COMMAND_BLOCKED): nesse caso o cofre degrada
// para ErrUnavailable e o teste faz skip registrando o erro exato vindo do
// sistema — nenhuma licença é tomada sobre o dado.
func TestVaultIntegrationWindows(t *testing.T) {
	if os.Getenv("DISCOVERY_TPM_TEST") != "1" {
		t.Skip("defina DISCOVERY_TPM_TEST=1 para executar o teste de integração com o TPM real")
	}

	ekHash := tpmEKFingerprint(t)
	t.Logf("fingerprint do teste: EK=%q", ekHash)

	v := Open(Options{
		Enabled:     true,
		Logf:        func(s string) { t.Logf("vault: %s", s) },
		Fingerprint: func() (string, string) { return ekHash, "" },
	})

	// Limpeza inicial idempotente.
	if err := v.Erase(); err != nil {
		t.Fatalf("Erase inicial: %v", err)
	}
	if rec, ok, err := v.Load(); err != nil || ok {
		t.Fatalf("Load após Erase inicial = (%+v, %v, %v); quer ok=false, err=nil", rec, ok, err)
	}
	t.Logf("Status inicial: %+v", v.Status())

	value, err := FormatValue("mdz_integrationtest01", "api.integration.test", true)
	if err != nil {
		t.Fatalf("FormatValue: %v", err)
	}
	want := Record{Schema: Schema, APIURL: value, AllowInsecureTLS: true}

	if err := v.Save(want); err != nil {
		if errors.Is(err, ErrUnavailable) {
			t.Skipf("NV_DefineSpace/NV_Write recusados pelo TBS fora de SYSTEM/elevação (degradação graciosa, sem panic): %v | Status=%+v", err, v.Status())
		}
		t.Fatalf("Save: %v", err)
	}
	t.Cleanup(func() {
		if err := v.Erase(); err != nil {
			t.Errorf("Erase de limpeza: %v", err)
		}
	})

	got, ok, err := v.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !ok {
		t.Fatal("Load: registro ausente após Save")
	}
	if got != want {
		t.Fatalf("registro divergente: got=%+v want=%+v", got, want)
	}

	// Save idempotente.
	if err := v.Save(want); err != nil {
		t.Fatalf("Save repetido: %v", err)
	}
	got2, ok2, err := v.Load()
	if err != nil || !ok2 || got2 != want {
		t.Fatalf("Load após Save repetido = (%+v, %v, %v)", got2, ok2, err)
	}

	// Status nunca expõe o token.
	st := v.Status()
	if st.Present == false || st.Host != "api.integration.test" || st.TokenPrefix != "mdz_integrat..." {
		t.Fatalf("Status inesperado: %+v", st)
	}
	if bytes.Contains([]byte(st.TokenPrefix), []byte("integrationtest01")) {
		t.Fatalf("Status vazou o token: %+v", st)
	}

	if err := v.Erase(); err != nil {
		t.Fatalf("Erase: %v", err)
	}
	if _, ok, err := v.Load(); err != nil || ok {
		t.Fatalf("Load após Erase = (_, %v, %v); quer ok=false, err=nil", ok, err)
	}
	if err := v.Erase(); err != nil {
		t.Fatalf("Erase idempotente: %v", err)
	}
}

// TestSealUnsealRealTPMWindows valida o caminho de selagem/desselagem no TPM
// real SEM depender de NV_DefineSpace (que exige SYSTEM/elevação). É a evidência
// de que a receita do probe (policy session + KeyedHash sem SensitiveDataOrigin)
// está corretamente implementada no pacote.
func TestSealUnsealRealTPMWindows(t *testing.T) {
	if os.Getenv("DISCOVERY_TPM_TEST") != "1" {
		t.Skip("defina DISCOVERY_TPM_TEST=1 para executar o teste de integração com o TPM real")
	}

	sum := sha256.Sum256([]byte(authPrefix + "integration-seal-test"))
	auth := sum[:]
	payload := []byte(`{"schema":"discovery.tpmvault/v1","API|URL":"mdz_sealtest0001@api.integration.test"}`)

	var privateBlob, publicBlob []byte
	if err := withTPM("selagem de teste", func(tp transport.TPM) error {
		var err error
		privateBlob, publicBlob, err = sealPayload(tp, auth, payload)
		return err
	}); err != nil {
		t.Fatalf("sealPayload: %v", err)
	}
	t.Logf("blob selado: private=%d bytes, public=%d bytes", len(privateBlob), len(publicBlob))

	env, err := encodeEnvelope(envelopeData{
		Flags:      envelopeFlagInsecureTLS,
		PayloadLen: uint16(len(payload)),
		Private:    privateBlob,
		Public:     publicBlob,
	})
	if err != nil {
		t.Fatalf("encodeEnvelope do blob real: %v", err)
	}
	if len(env) > nvDataSize {
		t.Fatalf("envelope real de %d bytes não cabe no índice NV de %d bytes", len(env), nvDataSize)
	}
	t.Logf("envelope real: %d bytes (índice NV comporta %d)", len(env), nvDataSize)

	if err := withTPM("desselagem de teste", func(tp transport.TPM) error {
		got, err := unsealPayload(tp, auth, privateBlob, publicBlob)
		if err != nil {
			return err
		}
		if !bytes.Equal(got, payload) {
			return fmt.Errorf("payload desselado difere: %q", got)
		}
		return nil
	}); err != nil {
		t.Fatalf("unsealPayload: %v", err)
	}

	// Auth errada precisa falhar (o objeto selado herda a auth derivada).
	if err := withTPM("desselagem com auth errada", func(tp transport.TPM) error {
		wrong := sha256.Sum256([]byte("outra-auth"))
		if _, err := unsealPayload(tp, wrong[:], privateBlob, publicBlob); err == nil {
			return errors.New("Unseal com auth errada deveria falhar")
		}
		return nil
	}); err != nil {
		t.Fatalf("teste de auth errada: %v", err)
	}
}

// tpmEKFingerprint reproduz app/services/hardwareid: hash SHA-256 da chave
// pública da EK (CreatePrimary sob RHEndorsement com o template RSA padrão).
// Não spawna processo algum.
func tpmEKFingerprint(t *testing.T) string {
	t.Helper()
	var hash string
	if err := withTPM("fingerprint da EK", func(tp transport.TPM) error {
		rsp, err := tpm2.CreatePrimary{
			PrimaryHandle: tpm2.TPMRHEndorsement,
			InPublic:      tpm2.New2B(tpm2.RSAEKTemplate),
		}.Execute(tp)
		if err != nil {
			return fmt.Errorf("CreatePrimary(EK): %w", err)
		}
		defer flushContext(tp, rsp.ObjectHandle)

		pub, err := rsp.OutPublic.Contents()
		if err != nil {
			return fmt.Errorf("decodificar OutPublic da EK: %w", err)
		}
		var pubBytes []byte
		switch pub.Type {
		case tpm2.TPMAlgRSA:
			rsaPub, err := pub.Unique.RSA()
			if err != nil {
				return err
			}
			pubBytes = rsaPub.Buffer
		case tpm2.TPMAlgECC:
			eccPub, err := pub.Unique.ECC()
			if err != nil {
				return err
			}
			pubBytes = append(append([]byte(nil), eccPub.X.Buffer...), eccPub.Y.Buffer...)
		default:
			return fmt.Errorf("algoritmo de EK não suportado: %v", pub.Type)
		}
		sum := sha256.Sum256(pubBytes)
		hash = hex.EncodeToString(sum[:])
		return nil
	}); err != nil {
		t.Skipf("TPM/EK indisponível; integração não pode rodar: %v", err)
	}
	return hash
}
