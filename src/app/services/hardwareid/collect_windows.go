//go:build windows

package hardwareid

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/google/go-tpm/tpm2"
	"github.com/google/go-tpm/tpm2/transport"
	"github.com/google/go-tpm/tpmutil"

	"discovery/app/core/processutil"
)

// collect coleta as identidades de hardware da máquina:
// a Endorsement Key (EK) do TPM 2.0 e o UUID do SMBIOS.
func collect() Info {
	info := Info{}

	// 1) TPM 2.0 Endorsement Key (EK)
	ek, ekAlg, err := readTPMEndorsementKey()
	if err != nil {
		info.TPMEKError = err.Error()
	} else {
		info.TPMEK = ek
		info.TPMEKAlg = ekAlg
		info.TPMEKAvailable = true
	}

	// 2) UUID SMBIOS (fallback / identidade complementar)
	uuid, err := readSMBIOSUUID()
	if err != nil {
		info.SMBIOSUUIDError = err.Error()
	} else {
		info.SMBIOSUUID = uuid
		info.SMBIOSUUIDAvailable = true
	}

	return info
}

// readTPMEndorsementKey abre o TPM 2.0 via TBS (tbs.dll), cria a Endorsement
// Key a partir do template padrão e retorna o hash SHA-256 da chave pública.
// Usa apenas a biblioteca github.com/google/go-tpm (sem dependências extras).
func readTPMEndorsementKey() (hashHex, alg string, err error) {
	// A chamada ao TPM pode travar se o dispositivo estiver indisponível.
	// Executa em goroutine com timeout para não bloquear a UI indefinidamente.
	const tpmTimeout = 10 * time.Second

	type result struct {
		hash string
		alg  string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		hash, alg, err := readTPMEndorsementKeyBlocking()
		ch <- result{hash: hash, alg: alg, err: err}
	}()

	select {
	case r := <-ch:
		return r.hash, r.alg, r.err
	case <-time.After(tpmTimeout):
		return "", "", fmt.Errorf("timeout ao acessar o TPM (%s)", tpmTimeout)
	}
}

// readTPMEndorsementKeyBlocking executa a leitura da EK de forma bloqueante.
func readTPMEndorsementKeyBlocking() (hashHex, alg string, err error) {
	rwc, err := tpmutil.OpenTPM()
	if err != nil {
		return "", "", fmt.Errorf("abrir TPM via TBS: %w", err)
	}
	defer rwc.Close()

	// Converte o io.ReadWriteCloser em transport.TPM para o tpm2.
	tpmDev := transport.FromReadWriteCloser(rwc)

	// Cria a EK no hierarchy de Endorsement usando o template RSA padrão.
	// A EK é determinística: o mesmo template + mesmo TPM gera sempre a mesma
	// chave pública, portanto o hash é estável entre execuções e formatações.
	createPrimary := tpm2.CreatePrimary{
		PrimaryHandle: tpm2.TPMRHEndorsement,
		InPublic:      tpm2.New2B(tpm2.RSAEKTemplate),
	}
	rsp, err := createPrimary.Execute(tpmDev)
	if err != nil {
		return "", "", fmt.Errorf("criar EK (CreatePrimary): %w", err)
	}
	defer func() {
		_, _ = tpm2.FlushContext{FlushHandle: rsp.ObjectHandle}.Execute(tpmDev)
	}()

	pub, err := rsp.OutPublic.Contents()
	if err != nil {
		return "", "", fmt.Errorf("decodificar chave pública da EK: %w", err)
	}

	// Extrai a chave pública e calcula o hash SHA-256.
	// Para RSA, serializamos o módulo; para ECC, X||Y.
	var pubBytes []byte
	switch pub.Type {
	case tpm2.TPMAlgRSA:
		rsaPub, err := pub.Unique.RSA()
		if err != nil {
			return "", "", fmt.Errorf("extrair chave RSA da EK: %w", err)
		}
		pubBytes = rsaPub.Buffer
		alg = "RSA"
	case tpm2.TPMAlgECC:
		eccPub, err := pub.Unique.ECC()
		if err != nil {
			return "", "", fmt.Errorf("extrair chave ECC da EK: %w", err)
		}
		pubBytes = append(append([]byte{}, eccPub.X.Buffer...), eccPub.Y.Buffer...)
		alg = "ECC"
	default:
		return "", "", fmt.Errorf("algoritmo de EK não suportado: %v", pub.Type)
	}

	sum := sha256.Sum256(pubBytes)
	return hex.EncodeToString(sum[:]), alg, nil
}

// readSMBIOSUUID obtém o UUID do SMBIOS via WMI (Win32_ComputerSystemProduct.UUID).
// É o identificador persistente da placa-mãe, usado como fallback quando não há TPM.
func readSMBIOSUUID() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	script := `$ErrorActionPreference = 'Stop'
$p = Get-CimInstance Win32_ComputerSystemProduct -ErrorAction SilentlyContinue
if ($p -and $p.UUID) { [string]$p.UUID } else { '' }`

	cmd := exec.CommandContext(ctx, "powershell",
		"-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden",
		"-Command", script)
	processutil.HideWindow(cmd)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("WMI Win32_ComputerSystemProduct: %w", err)
	}

	uuid := strings.TrimSpace(string(output))
	// Remove possíveis avisos/prefixos do PowerShell e normaliza o UUID.
	uuid = sanitizeSMBIOSUUID(uuid)
	if uuid == "" {
		return "", fmt.Errorf("UUID SMBIOS não disponível (vazio)")
	}
	return uuid, nil
}

// sanitizeSMBIOSUUID normaliza o UUID retornado pelo WMI, removendo caracteres
// indesejados (ex.: avisos do PowerShell, quebras de linha extras) e mantendo
// apenas o formato canônico do UUID (hex + hífens).
func sanitizeSMBIOSUUID(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F', r == '-':
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}
