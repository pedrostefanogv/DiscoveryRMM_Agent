//go:build windows

// Command tpmvault-probe valida, em uma máquina real, a viabilidade de um
// cofre TPM 2.0 para o agente Discovery (WS0 do plano aprovado).
//
// Sequência executada:
//
//  1. abertura do TPM via TBS (tpm2/transport/windowstpm, o transporte
//     mantido do go-tpm — valida que o dispositivo é TPM 2.0);
//  2. NVReadPublic do índice alvo (padrão 0x0180D15C);
//  3. NVDefineSpace sob o owner, com authValue derivado de 32 bytes
//     sha256("discovery.tpmvault|v1|" + fingerprint);
//  4. NVWrite + NVRead em blocos de 512 bytes, autorizados pelo próprio índice,
//     com comparação byte-exata;
//  5. selagem (CreatePrimary/EK -> Create -> Load -> Unseal) e comparação;
//     5b. verificação da selagem após reiniciar o processo (o blob persistido por
//     uma execução é carregado e desselado pela execução seguinte);
//  6. NVUndefineSpace para limpar o ambiente (desativável com --keep-index).
//
// O relatório final é um JSON com um campo por passo.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"github.com/google/go-tpm/tpm2"
	"github.com/google/go-tpm/tpm2/transport"
	"github.com/google/go-tpm/tpm2/transport/windowstpm"
	"golang.org/x/sys/windows"
)

const (
	// tpmTimeout limita cada operação de TPM (goroutine + time.After), como em
	// app/services/hardwareid/collect_windows.go.
	tpmTimeout   = 10 * time.Second
	defaultIndex = 0x0180D15C
	nvDataSize   = 768
	nvChunkSize  = 512
	authPrefix   = "discovery.tpmvault|v1|"
)

// procGetSystemFirmwareTable acessa a tabela SMBIOS exposta pelo firmware.
var procGetSystemFirmwareTable = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetSystemFirmwareTable")

// firmwareTableRSMB é a assinatura 'RSMB' do provedor de tabela SMBIOS.
const firmwareTableRSMB = 0x424D5352

// knownRCs permite anexar o nome canônico do TPM_RC à mensagem de erro.
var knownRCs = []struct {
	name string
	rc   tpm2.TPMRC
}{
	{"TPM_RC_HANDLE", tpm2.TPMRCHandle},
	{"TPM_RC_HIERARCHY", tpm2.TPMRCHierarchy},
	{"TPM_RC_AUTH_FAIL", tpm2.TPMRCAuthFail},
	{"TPM_RC_BAD_AUTH", tpm2.TPMRCBadAuth},
	{"TPM_RC_AUTH_UNAVAILABLE", tpm2.TPMRCAuthUnavailable},
	{"TPM_RC_POLICY_FAIL", tpm2.TPMRCPolicyFail},
	{"TPM_RC_ATTRIBUTES", tpm2.TPMRCAttributes},
	{"TPM_RC_NV_DEFINED", tpm2.TPMRCNVDefined},
	{"TPM_RC_NV_RANGE", tpm2.TPMRCNVRange},
	{"TPM_RC_NV_SIZE", tpm2.TPMRCNVSize},
	{"TPM_RC_NV_SPACE", tpm2.TPMRCNVSpace},
	{"TPM_RC_NV_AUTHORIZATION", tpm2.TPMRCNVAuthorization},
	{"TPM_RC_NV_LOCKED", tpm2.TPMRCNVLocked},
	{"TPM_RC_NV_UNINITIALIZED", tpm2.TPMRCNVUninitialized},
	{"TPM_RC_VALUE", tpm2.TPMRCValue},
	{"TPM_RC_SIZE", tpm2.TPMRCSize},
	{"TPM_RC_INTEGRITY", tpm2.TPMRCIntegrity},
}

// stepResult é o resultado de um passo do probe.
type stepResult struct {
	OK     bool   `json:"ok"`
	State  string `json:"state,omitempty"`
	Error  string `json:"error,omitempty"`
	RC     string `json:"rc,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// sealAttempt registra uma tentativa de selagem/desselagem.
type sealAttempt struct {
	Label           string `json:"label"`
	OK              bool   `json:"ok"`
	Error           string `json:"error,omitempty"`
	RC              string `json:"rc,omitempty"`
	UnsealedMatched bool   `json:"unsealed_matched"`
}

// sealResult é o resultado do passo de selagem.
type sealResult struct {
	OK       bool          `json:"ok"`
	Profile  string        `json:"profile,omitempty"`
	Attempts []sealAttempt `json:"attempts,omitempty"`
	Error    string        `json:"error,omitempty"`
	RC       string        `json:"rc,omitempty"`
	Detail   string        `json:"detail,omitempty"`

	// blobCache carrega o blob selado desta execução para o passo 5b.
	blobCache []byte
}

// fingerprint identifica a máquina que deriva a auth do cofre.
type fingerprint struct {
	Source string `json:"source"`
	Value  string `json:"value,omitempty"`
	Alg    string `json:"alg,omitempty"`
	Error  string `json:"error,omitempty"`
}

// persistedBlob é o blob selado persistido entre processos (passo 5b).
type persistedBlob struct {
	Profile string `json:"profile"`
	Payload string `json:"payload_b64"`
	Public  string `json:"public_b64"`
	Private string `json:"private_b64"`
}

// report é o relatório final: um campo por passo.
type report struct {
	Tool               string      `json:"tool"`
	Timestamp          string      `json:"timestamp"`
	Elevated           bool        `json:"elevated"`
	Index              string      `json:"index"`
	KeepIndex          bool        `json:"keep_index"`
	PayloadBytes       int         `json:"payload_bytes"`
	Fingerprint        fingerprint `json:"fingerprint"`
	Step1OpenTPM       stepResult  `json:"step1_open_tpm"`
	Step2NVReadPublic  stepResult  `json:"step2_nv_read_public"`
	Step3NVDefineSpace stepResult  `json:"step3_nv_define_space"`
	Step4NVWriteRead   stepResult  `json:"step4_nv_write_read"`
	Step5SealUnseal    sealResult  `json:"step5_seal_unseal"`
	Step5bAfterRestart stepResult  `json:"step5b_seal_after_restart"`
	Step6NVUndefine    stepResult  `json:"step6_nv_undefine"`
	Step7Verdict       string      `json:"step7_verdict"`
	Summary            string      `json:"summary"`
}

func main() {
	keepIndex := flag.Bool("keep-index", false, "não remover o índice NV ao final do probe")
	indexFlag := flag.String("index", fmt.Sprintf("0x%08X", defaultIndex), "handle do índice NV (hex) a ser testado")
	flag.Parse()

	index, err := parseIndex(*indexFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tpmvault-probe: %v\n", err)
		os.Exit(2)
	}

	rep := report{
		Tool:         "discovery-tpmvault-probe",
		Timestamp:    time.Now().Format(time.RFC3339),
		Elevated:     isElevated(),
		Index:        fmt.Sprintf("0x%08X", index),
		KeepIndex:    *keepIndex,
		PayloadBytes: len(testPayload()),
	}

	run(&rep, index)

	out, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "tpmvault-probe: falha ao serializar relatório: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(out))
}

func parseIndex(s string) (uint32, error) {
	clean := strings.TrimSpace(s)
	clean = strings.TrimPrefix(clean, "0x")
	clean = strings.TrimPrefix(clean, "0X")
	v, err := strconv.ParseUint(clean, 16, 32)
	if err != nil {
		return 0, fmt.Errorf("--index inválido (%q): esperado hexadecimal de 32 bits", s)
	}
	return uint32(v), nil
}

// testPayload é o JSON mínimo de ~80 bytes usado nos passos 4/5/5b.
func testPayload() []byte {
	return []byte("{\"schema\":\"discovery.tpmvault/v1\",\"API|URL\":\"mdz_probe@exemplo\",\"note\":\"ws0\"}")
}

func run(rep *report, index uint32) {
	dev, closeTPM, err := openTPM()
	if err != nil {
		rep.Step1OpenTPM = failStep(err)
		rep.Step7Verdict = "NO-GO"
		rep.Summary = "Não foi possível abrir o TPM via TBS; nenhum passo executado."
		return
	}
	rep.Step1OpenTPM = stepResult{OK: true, Detail: "windowstpm.Open (TBS) OK"}
	defer closeTPM()

	fp, auth := resolveFingerprint(dev)
	rep.Fingerprint = fp

	// Passo 2 — NVReadPublic.
	defined := false
	pubRsp, err := tpmExec("NVReadPublic", func() (*tpm2.NVReadPublicResponse, error) {
		return tpm2.NVReadPublic{NVIndex: tpm2.TPMHandle(index)}.Execute(dev)
	})
	switch {
	case err == nil:
		defined = true
		rep.Step2NVReadPublic = stepResult{
			OK:     true,
			State:  "defined",
			Detail: fmt.Sprintf("índice já definido; name=%x", pubRsp.NVName.Buffer),
		}
	case errors.Is(err, tpm2.TPMRCHandle):
		rep.Step2NVReadPublic = stepResult{
			OK:     true,
			State:  "undefined",
			RC:     tpmRC(err),
			Detail: "índice não definido (TPM_RC_HANDLE), como esperado em ambiente limpo",
		}
	default:
		rep.Step2NVReadPublic = failStep(err)
	}

	// Passo 3 — NVDefineSpace (com NVUndefineSpace prévio se já existir).
	definedByUs := false
	if !rep.Step2NVReadPublic.OK {
		rep.Step3NVDefineSpace = stepResult{OK: false, State: "skipped", Error: "NVReadPublic falhou; NVDefineSpace não tentado"}
	} else {
		preOK := true
		if defined {
			_, uerr := tpmExec("NVUndefineSpace (limpeza prévia)", func() (*tpm2.NVUndefineSpaceResponse, error) {
				return tpm2.NVUndefineSpace{AuthHandle: tpm2.TPMRHOwner, NVIndex: tpm2.TPMHandle(index)}.Execute(dev)
			})
			if uerr != nil {
				msg, rc := describeTPMError(uerr)
				preOK = false
				rep.Step3NVDefineSpace = stepResult{
					OK:     false,
					State:  "predefined-remove-failed",
					Error:  "índice já existia e NVUndefineSpace sob owner falhou: " + msg,
					RC:     rc,
					Detail: "o índice pré-existente não pôde ser removido",
				}
			}
		}
		if preOK {
			_, derr := tpmExec("NVDefineSpace", func() (*tpm2.NVDefineSpaceResponse, error) {
				return nvDefineSpace(index, auth).Execute(dev)
			})
			if derr != nil {
				msg, rc := describeTPMError(derr)
				rep.Step3NVDefineSpace = stepResult{OK: false, State: "error", Error: msg, RC: rc}
			} else {
				definedByUs = true
				rep.Step3NVDefineSpace = stepResult{
					OK:    true,
					State: "defined",
					Detail: fmt.Sprintf(
						"NVDefineSpace OK (owner; authValue derivado de %d bytes; DataSize=%d; TPMNTOrdinary/AuthRead/AuthWrite/OwnerRead/OwnerWrite/NoDA)",
						len(auth), nvDataSize),
				}
			}
		}
	}

	// Passo 4 — NVWrite + NVRead.
	payload := testPayload()
	if definedByUs {
		rep.Step4NVWriteRead = nvWriteRead(dev, index, auth, payload)
	} else {
		rep.Step4NVWriteRead = stepResult{OK: false, State: "skipped", Error: "NVDefineSpace não concluiu; NVWrite/NVRead não tentados"}
	}

	// Passo 5 — selagem.
	rep.Step5SealUnseal = sealUnseal(dev, auth, payload)

	// Passo 5b — selagem após reiniciar o processo.
	rep.Step5bAfterRestart = sealAfterRestart(dev, auth, payload, rep.Step5SealUnseal)

	// Passo 6 — limpeza.
	switch {
	case !definedByUs:
		rep.Step6NVUndefine = stepResult{OK: false, State: "skipped", Error: "índice não foi criado por este probe"}
	case rep.KeepIndex:
		rep.Step6NVUndefine = stepResult{OK: true, State: "kept", Detail: "--keep-index: índice mantido"}
	default:
		_, uerr := tpmExec("NVUndefineSpace", func() (*tpm2.NVUndefineSpaceResponse, error) {
			return tpm2.NVUndefineSpace{AuthHandle: tpm2.TPMRHOwner, NVIndex: tpm2.TPMHandle(index)}.Execute(dev)
		})
		if uerr != nil {
			msg, rc := describeTPMError(uerr)
			rep.Step6NVUndefine = stepResult{OK: false, State: "error", Error: msg, RC: rc}
		} else {
			rep.Step6NVUndefine = stepResult{OK: true, State: "undefined", Detail: "índice removido"}
		}
	}

	// Veredito.
	switch {
	case rep.Step5SealUnseal.OK && rep.Step4NVWriteRead.OK:
		rep.Step7Verdict = "GO"
	case rep.Step5SealUnseal.OK:
		rep.Step7Verdict = "GO_PARCIAL"
	default:
		rep.Step7Verdict = "NO-GO"
	}
	rep.Summary = buildSummary(rep)
}

func openTPM() (transport.TPM, func(), error) {
	dev, err := tpmExec("OpenTPM (TBS)", func() (transport.TPMCloser, error) {
		return windowstpm.Open()
	})
	if err != nil {
		return nil, nil, err
	}
	return dev, func() { _ = dev.Close() }, nil
}

// resolveFingerprint calcula o fingerprint da máquina (hash da EK, como em
// collect_windows.go, ou UUID SMBIOS lido da tabela de firmware) e a auth
// derivada do cofre.
func resolveFingerprint(dev transport.TPM) (fingerprint, []byte) {
	fp := fingerprint{}
	value, alg, err := ekFingerprint(dev)
	if err == nil {
		fp.Source, fp.Value, fp.Alg = "ek", value, alg
	} else {
		uuid, uerr := smbiosUUID()
		if uerr != nil {
			fp.Source = "none"
			fp.Error = fmt.Sprintf("EK indisponível: %v; UUID SMBIOS indisponível: %v", err, uerr)
		} else {
			fp.Source, fp.Value = "smbios", uuid
			fp.Error = "EK indisponível, usando fallback SMBIOS: " + err.Error()
		}
	}
	sum := sha256.Sum256([]byte(authPrefix + fp.Value))
	return fp, sum[:]
}

// ekFingerprint reproduz app/services/hardwareid/collect_windows.go:
// CreatePrimary(RHEndorsement, RSAEKTemplate) e sha256 da chave pública.
func ekFingerprint(dev transport.TPM) (string, string, error) {
	rsp, err := tpmExec("CreatePrimary(EK)", func() (*tpm2.CreatePrimaryResponse, error) {
		return tpm2.CreatePrimary{
			PrimaryHandle: tpm2.TPMRHEndorsement,
			InPublic:      tpm2.New2B(tpm2.RSAEKTemplate),
		}.Execute(dev)
	})
	if err != nil {
		return "", "", fmt.Errorf("criar EK (CreatePrimary): %w", err)
	}
	defer flushContext(dev, rsp.ObjectHandle)

	pub, err := rsp.OutPublic.Contents()
	if err != nil {
		return "", "", fmt.Errorf("decodificar chave pública da EK: %w", err)
	}
	var pubBytes []byte
	var alg string
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

func flushContext(dev transport.TPM, handle tpm2.TPMHandle) {
	_, _ = tpmExec("FlushContext", func() (*tpm2.FlushContextResponse, error) {
		return tpm2.FlushContext{FlushHandle: handle}.Execute(dev)
	})
}

func nvDefineSpace(index uint32, auth []byte) tpm2.NVDefineSpace {
	return tpm2.NVDefineSpace{
		AuthHandle: tpm2.TPMRHOwner,
		Auth:       tpm2.TPM2BAuth{Buffer: auth},
		PublicInfo: tpm2.New2B(tpm2.TPMSNVPublic{
			NVIndex: tpm2.TPMHandle(index),
			NameAlg: tpm2.TPMAlgSHA256,
			Attributes: tpm2.TPMANV{
				OwnerWrite:     true,
				OwnerRead:      true,
				AuthWrite:      true,
				AuthRead:       true,
				NT:             tpm2.TPMNTOrdinary,
				NoDA:           true,
				PlatformCreate: false,
			},
			DataSize: nvDataSize,
		}),
	}
}

// nvIndexName lê o Name atual do índice (muda quando TPMA_NV_WRITTEN é setado).
func nvIndexName(dev transport.TPM, index uint32) (tpm2.TPM2BName, error) {
	rsp, err := tpmExec("NVReadPublic", func() (*tpm2.NVReadPublicResponse, error) {
		return tpm2.NVReadPublic{NVIndex: tpm2.TPMHandle(index)}.Execute(dev)
	})
	if err != nil {
		return tpm2.TPM2BName{}, err
	}
	return rsp.NVName, nil
}

// nvWriteRead grava o payload em blocos e lê de volta autorizando pelo próprio
// índice, como em tpm2/test/nv_test.go.
func nvWriteRead(dev transport.TPM, index uint32, auth, payload []byte) stepResult {
	name, err := nvIndexName(dev, index)
	if err != nil {
		msg, rc := describeTPMError(err)
		return stepResult{OK: false, State: "error", Error: "NVReadPublic (pré-escrita): " + msg, RC: rc}
	}

	idxAuth := tpm2.AuthHandle{
		Handle: tpm2.TPMHandle(index),
		Name:   name,
		Auth:   tpm2.PasswordAuth(auth),
	}
	idxNamed := tpm2.NamedHandle{Handle: tpm2.TPMHandle(index), Name: name}

	written := 0
	for off := 0; off < len(payload); off += nvChunkSize {
		end := off + nvChunkSize
		if end > len(payload) {
			end = len(payload)
		}
		chunk := payload[off:end]
		_, werr := tpmExec("NVWrite", func() (*tpm2.NVWriteResponse, error) {
			return tpm2.NVWrite{
				AuthHandle: idxAuth,
				NVIndex:    idxNamed,
				Data:       tpm2.TPM2BMaxNVBuffer{Buffer: chunk},
				Offset:     uint16(off),
			}.Execute(dev)
		})
		if werr != nil {
			msg, rc := describeTPMError(werr)
			return stepResult{OK: false, State: "write-error", Error: fmt.Sprintf("NVWrite (offset %d): %s", off, msg), RC: rc}
		}
		written += len(chunk)
	}

	// Após a primeira escrita o Name do índice incorpora TPMA_NV_WRITTEN.
	nameAfter, err := nvIndexName(dev, index)
	if err != nil {
		msg, rc := describeTPMError(err)
		return stepResult{OK: false, State: "error", Error: "NVReadPublic (pós-escrita): " + msg, RC: rc}
	}
	readAuth := tpm2.AuthHandle{
		Handle: tpm2.TPMHandle(index),
		Name:   nameAfter,
		Auth:   tpm2.PasswordAuth(auth),
	}
	readNamed := tpm2.NamedHandle{Handle: tpm2.TPMHandle(index), Name: nameAfter}

	got := make([]byte, 0, written)
	for off := 0; off < written; off += nvChunkSize {
		size := written - off
		if size > nvChunkSize {
			size = nvChunkSize
		}
		readRsp, rerr := tpmExec("NVRead", func() (*tpm2.NVReadResponse, error) {
			return tpm2.NVRead{
				AuthHandle: readAuth,
				NVIndex:    readNamed,
				Size:       uint16(size),
				Offset:     uint16(off),
			}.Execute(dev)
		})
		if rerr != nil {
			msg, rc := describeTPMError(rerr)
			return stepResult{OK: false, State: "read-error", Error: fmt.Sprintf("NVRead (offset %d): %s", off, msg), RC: rc}
		}
		got = append(got, readRsp.Data.Buffer...)
	}

	if !bytes.Equal(got, payload) {
		return stepResult{
			OK:     false,
			State:  "mismatch",
			Error:  fmt.Sprintf("leitura difere do payload: %d bytes lidos de %d gravados", len(got), written),
			Detail: fmt.Sprintf("esperado=%q obtido=%q", payload, got),
		}
	}
	return stepResult{
		OK:     true,
		State:  "match",
		Detail: fmt.Sprintf("NVWrite+NVRead byte-exata (%d bytes em blocos de %d, auth pelo próprio índice)", len(payload), nvChunkSize),
	}
}

// sealProfile descreve uma combinação testada no passo 5. A primeira é
// exatamente a prescrita no plano; as demais isolam a causa de uma falha.
type sealProfile struct {
	label           string
	useEKPolicy     bool
	sensitiveOrigin bool
}

var sealProfiles = []sealProfile{
	{"ek_password_spec_attrs", false, true},
	{"ek_policy_spec_attrs", true, true},
	{"ek_password_reference_attrs", false, false},
	{"ek_policy_reference_attrs", true, false},
}

func sealUnseal(dev transport.TPM, auth, payload []byte) sealResult {
	res := sealResult{}
	for _, p := range sealProfiles {
		attempt, blob := trySeal(dev, p, auth, payload)
		res.Attempts = append(res.Attempts, attempt)
		if attempt.OK {
			res.OK = true
			res.Profile = p.label
			res.Detail = "selagem/desselagem validada com o perfil " + p.label
			res.blobCache = blob
			return res
		}
	}
	res.Error = "nenhum perfil de selagem funcionou"
	return res
}

// trySeal executa CreatePrimary -> Create -> Load -> Unseal para um perfil.
func trySeal(dev transport.TPM, p sealProfile, objAuth, payload []byte) (sealAttempt, []byte) {
	attempt := sealAttempt{Label: p.label}

	prim, err := tpmExec("CreatePrimary(EK)", func() (*tpm2.CreatePrimaryResponse, error) {
		return tpm2.CreatePrimary{
			PrimaryHandle: tpm2.TPMRHEndorsement,
			InPublic:      tpm2.New2B(tpm2.RSAEKTemplate),
		}.Execute(dev)
	})
	if err != nil {
		attempt.Error, attempt.RC = describeTPMError(err)
		return attempt, nil
	}
	defer flushContext(dev, prim.ObjectHandle)

	var parentAuth tpm2.Session
	if p.useEKPolicy {
		parentAuth = tpm2.Policy(tpm2.TPMAlgSHA256, 16, endorsementPolicy)
	}

	attrs := tpm2.TPMAObject{
		FixedTPM:     true,
		FixedParent:  true,
		UserWithAuth: true,
		NoDA:         true,
	}
	if p.sensitiveOrigin {
		attrs.SensitiveDataOrigin = true
	}

	createCmd := tpm2.Create{
		ParentHandle: tpm2.AuthHandle{Handle: prim.ObjectHandle, Name: prim.Name, Auth: parentAuth},
		InSensitive: tpm2.TPM2BSensitiveCreate{
			Sensitive: &tpm2.TPMSSensitiveCreate{
				UserAuth: tpm2.TPM2BAuth{Buffer: objAuth},
				Data: tpm2.NewTPMUSensitiveCreate(&tpm2.TPM2BSensitiveData{
					Buffer: payload,
				}),
			},
		},
		InPublic: tpm2.New2B(tpm2.TPMTPublic{
			Type:             tpm2.TPMAlgKeyedHash,
			NameAlg:          tpm2.TPMAlgSHA256,
			ObjectAttributes: attrs,
		}),
	}
	blobRsp, err := tpmExec("Create(sealed)", func() (*tpm2.CreateResponse, error) {
		return createCmd.Execute(dev)
	})
	if err != nil {
		attempt.Error, attempt.RC = describeTPMError(err)
		return attempt, nil
	}

	loadRsp, err := tpmExec("Load(sealed)", func() (*tpm2.LoadResponse, error) {
		return tpm2.Load{
			ParentHandle: tpm2.AuthHandle{Handle: prim.ObjectHandle, Name: prim.Name, Auth: parentAuth},
			InPrivate:    blobRsp.OutPrivate,
			InPublic:     blobRsp.OutPublic,
		}.Execute(dev)
	})
	if err != nil {
		attempt.Error, attempt.RC = describeTPMError(err)
		return attempt, nil
	}
	defer flushContext(dev, loadRsp.ObjectHandle)

	unsealRsp, err := tpmExec("Unseal", func() (*tpm2.UnsealResponse, error) {
		return tpm2.Unseal{
			ItemHandle: tpm2.AuthHandle{
				Handle: loadRsp.ObjectHandle,
				Name:   loadRsp.Name,
				Auth:   tpm2.PasswordAuth(objAuth),
			},
		}.Execute(dev)
	})
	if err != nil {
		attempt.Error, attempt.RC = describeTPMError(err)
		return attempt, nil
	}

	attempt.UnsealedMatched = bytes.Equal(unsealRsp.OutData.Buffer, payload)
	if !attempt.UnsealedMatched {
		attempt.Error = fmt.Sprintf("payload desselado difere: esperado %d bytes, obtido %d", len(payload), len(unsealRsp.OutData.Buffer))
		return attempt, nil
	}
	attempt.OK = true

	cached, err := json.Marshal(persistedBlob{
		Profile: p.label,
		Payload: base64.StdEncoding.EncodeToString(payload),
		Public:  base64.StdEncoding.EncodeToString(tpm2.Marshal(blobRsp.OutPublic)),
		Private: base64.StdEncoding.EncodeToString(tpm2.Marshal(blobRsp.OutPrivate)),
	})
	if err != nil {
		return attempt, nil
	}
	return attempt, cached
}

// endorsementPolicy satisfaz TPM2_PolicySecret(RH_ENDORSEMENT), a authPolicy do
// RSAEKTemplate (ver tpm2/test/ek_test.go).
func endorsementPolicy(t transport.TPM, handle tpm2.TPMISHPolicy, nonceTPM tpm2.TPM2BNonce) error {
	_, err := tpm2.PolicySecret{
		AuthHandle:    tpm2.TPMRHEndorsement,
		PolicySession: handle,
		NonceTPM:      nonceTPM,
	}.Execute(t)
	return err
}

func sealBlobPath() string {
	return filepath.Join(os.TempDir(), "discovery-tpmvault-probe-seal.json")
}

// sealAfterRestart valida o passo 5b: um blob selado por uma execução anterior
// precisa ser carregado e desselado por esta (nova) instância do processo.
// Quando ainda não existe blob, o desta execução é persistido para a próxima.
func sealAfterRestart(dev transport.TPM, auth, payload []byte, seal sealResult) stepResult {
	path := sealBlobPath()
	raw, err := os.ReadFile(path)
	if err != nil {
		if !seal.OK {
			return stepResult{OK: false, State: "skipped", Error: "selagem (passo 5) falhou; nada a persistir"}
		}
		if seal.blobCache == nil {
			return stepResult{OK: false, State: "error", Error: "blob selado indisponível para persistir"}
		}
		if werr := os.WriteFile(path, seal.blobCache, 0o600); werr != nil {
			return stepResult{OK: false, State: "error", Error: fmt.Sprintf("persistir blob para o próximo processo: %v", werr)}
		}
		return stepResult{
			OK:     true,
			State:  "persisted",
			Detail: fmt.Sprintf("blob selado persistido em %s; reexecute o probe para validar a carga em um processo novo", path),
		}
	}

	var blob persistedBlob
	if jerr := json.Unmarshal(raw, &blob); jerr != nil {
		return stepResult{OK: false, State: "error", Error: fmt.Sprintf("blob persistido ilegível (%s): %v", path, jerr)}
	}
	if seal.OK && blob.Profile != seal.Profile {
		return stepResult{
			OK:     false,
			State:  "profile-mismatch",
			Detail: fmt.Sprintf("blob persistido usa %s, execução atual usa %s", blob.Profile, seal.Profile),
		}
	}

	prim, err := tpmExec("CreatePrimary(EK)", func() (*tpm2.CreatePrimaryResponse, error) {
		return tpm2.CreatePrimary{
			PrimaryHandle: tpm2.TPMRHEndorsement,
			InPublic:      tpm2.New2B(tpm2.RSAEKTemplate),
		}.Execute(dev)
	})
	if err != nil {
		msg, rc := describeTPMError(err)
		return stepResult{OK: false, State: "error", Error: msg, RC: rc}
	}
	defer flushContext(dev, prim.ObjectHandle)

	pubBytes, err1 := base64.StdEncoding.DecodeString(blob.Public)
	privBytes, err2 := base64.StdEncoding.DecodeString(blob.Private)
	wantPayload, err3 := base64.StdEncoding.DecodeString(blob.Payload)
	if err1 != nil || err2 != nil || err3 != nil {
		return stepResult{OK: false, State: "error", Error: "campos base64 do blob persistido inválidos"}
	}
	outPublic, err := tpm2.Unmarshal[tpm2.TPM2BPublic, *tpm2.TPM2BPublic](pubBytes)
	if err != nil {
		return stepResult{OK: false, State: "error", Error: fmt.Sprintf("decodificar OutPublic persistido: %v", err)}
	}
	outPrivate, err := tpm2.Unmarshal[tpm2.TPM2BPrivate, *tpm2.TPM2BPrivate](privBytes)
	if err != nil {
		return stepResult{OK: false, State: "error", Error: fmt.Sprintf("decodificar OutPrivate persistido: %v", err)}
	}

	var parentAuth tpm2.Session
	if strings.Contains(blob.Profile, "policy") {
		parentAuth = tpm2.Policy(tpm2.TPMAlgSHA256, 16, endorsementPolicy)
	}

	loadRsp, err := tpmExec("Load(blob persistido)", func() (*tpm2.LoadResponse, error) {
		return tpm2.Load{
			ParentHandle: tpm2.AuthHandle{Handle: prim.ObjectHandle, Name: prim.Name, Auth: parentAuth},
			InPrivate:    *outPrivate,
			InPublic:     *outPublic,
		}.Execute(dev)
	})
	if err != nil {
		msg, rc := describeTPMError(err)
		return stepResult{OK: false, State: "error", Error: "Load do blob persistido: " + msg, RC: rc}
	}
	defer flushContext(dev, loadRsp.ObjectHandle)

	unsealRsp, err := tpmExec("Unseal(blob persistido)", func() (*tpm2.UnsealResponse, error) {
		return tpm2.Unseal{
			ItemHandle: tpm2.AuthHandle{
				Handle: loadRsp.ObjectHandle,
				Name:   loadRsp.Name,
				Auth:   tpm2.PasswordAuth(auth),
			},
		}.Execute(dev)
	})
	if err != nil {
		msg, rc := describeTPMError(err)
		return stepResult{OK: false, State: "error", Error: "Unseal do blob persistido: " + msg, RC: rc}
	}
	if !bytes.Equal(unsealRsp.OutData.Buffer, wantPayload) {
		return stepResult{OK: false, State: "mismatch", Error: "payload desselado difere do payload selado pelo processo anterior"}
	}
	_ = os.Remove(path)
	return stepResult{
		OK:    true,
		State: "match",
		Detail: fmt.Sprintf("novo processo carregou e desselou o blob de %s (perfil %s); %d bytes idênticos",
			path, blob.Profile, len(wantPayload)),
	}
}

// describeTPMError extrai mensagem legível e o TPM_RC numérico.
func describeTPMError(err error) (msg, rc string) {
	if err == nil {
		return "", ""
	}
	var code tpm2.TPMRC
	if errors.As(err, &code) {
		rc = fmt.Sprintf("0x%08X", uint32(code))
	}
	msg = err.Error()
	for _, k := range knownRCs {
		if errors.Is(err, k.rc) && !strings.Contains(msg, k.name) {
			msg = k.name + ": " + msg
		}
	}
	return msg, rc
}

func tpmRC(err error) string {
	_, rc := describeTPMError(err)
	return rc
}

func failStep(err error) stepResult {
	msg, rc := describeTPMError(err)
	return stepResult{OK: false, State: "error", Error: msg, RC: rc}
}

// tpmExec executa uma operação de TPM em goroutine com timeout de 10s.
func tpmExec[T any](name string, fn func() (T, error)) (T, error) {
	type outcome struct {
		value T
		err   error
	}
	ch := make(chan outcome, 1)
	go func() {
		v, err := fn()
		ch <- outcome{value: v, err: err}
	}()
	select {
	case o := <-ch:
		return o.value, o.err
	case <-time.After(tpmTimeout):
		var zero T
		return zero, fmt.Errorf("timeout de %s na operação %s", tpmTimeout, name)
	}
}

func buildSummary(rep *report) string {
	parts := []string{
		fmt.Sprintf("abrir TPM=%v", rep.Step1OpenTPM.OK),
		fmt.Sprintf("NVReadPublic=%s", stepState(rep.Step2NVReadPublic)),
		fmt.Sprintf("NVDefineSpace=%v", rep.Step3NVDefineSpace.OK),
		fmt.Sprintf("NVWrite/Read=%v", rep.Step4NVWriteRead.OK),
		fmt.Sprintf("selagem/unseal=%v", rep.Step5SealUnseal.OK),
		fmt.Sprintf("selagem após reinício=%s", stepState(rep.Step5bAfterRestart)),
		fmt.Sprintf("NVUndefine=%s", stepState(rep.Step6NVUndefine)),
	}
	if !rep.Elevated {
		parts = append(parts, "processo NÃO elevado")
	}
	return strings.Join(parts, "; ")
}

func stepState(s stepResult) string {
	if s.OK {
		if s.State != "" {
			return s.State
		}
		return "ok"
	}
	if s.State != "" {
		return s.State
	}
	return "fail"
}

func isElevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

// smbiosUUID lê o UUID do SMBIOS (estrutura tipo 1) direto da tabela de
// firmware — a mesma origem de Win32_ComputerSystemProduct.UUID no WMI, sem
// depender de um subprocesso do PowerShell. O binário não pode conter o literal
// "powershell.exe": nesta máquina o Kaspersky bloqueia o binário recém-linkado
// nesse caso (Access is denied no arquivo de saída do linker).
func smbiosUUID() (string, error) {
	size, _, _ := procGetSystemFirmwareTable.Call(firmwareTableRSMB, 0, 0, 0)
	if size == 0 {
		return "", errors.New("GetSystemFirmwareTable(RSMB) não retornou tamanho")
	}
	buf := make([]byte, size)
	n, _, callErr := procGetSystemFirmwareTable.Call(firmwareTableRSMB, 0, uintptr(unsafe.Pointer(&buf[0])), size)
	if n == 0 {
		return "", fmt.Errorf("GetSystemFirmwareTable(RSMB): %v", callErr)
	}
	data := buf[:n]
	if len(data) < 8 {
		return "", errors.New("tabela SMBIOS truncada")
	}
	major, minor := data[1], data[2]
	length := int(binary.LittleEndian.Uint32(data[4:8]))
	if length > len(data)-8 {
		length = len(data) - 8
	}
	return findSMBIOSUUID(data[8:8+length], major, minor)
}

// findSMBIOSUUID percorre as estruturas SMBIOS e formata o UUID da tipo 1.
func findSMBIOSUUID(table []byte, major, minor byte) (string, error) {
	for off := 0; off+4 <= len(table); {
		typ := table[off]
		l := int(table[off+1])
		if l < 4 || off+l > len(table) {
			break
		}
		if typ == 1 && l >= 24 {
			raw := table[off+8 : off+24]
			empty := true
			for _, b := range raw {
				if b != 0 {
					empty = false
					break
				}
			}
			if !empty {
				return formatSMBIOSUUID(raw, major, minor), nil
			}
		}
		// Avança a área formatada e a área de strings (terminada por NUL duplo).
		next := off + l
		for next+1 < len(table) && !(table[next] == 0 && table[next+1] == 0) {
			next++
		}
		next += 2
		if next <= off {
			break
		}
		off = next
		if typ == 0x7F {
			break
		}
	}
	return "", errors.New("UUID SMBIOS não encontrado na tabela SMBIOS")
}

// formatSMBIOSUUID usa a mesma representação do WMI/dmidecode: os três
// primeiros campos (DWORD + WORD + WORD) são little-endian.
func formatSMBIOSUUID(raw []byte, major, minor byte) string {
	b := make([]byte, 16)
	copy(b, raw)
	if major < 3 {
		b[0], b[1] = b[1], b[0]
		b[2], b[3] = b[3], b[2]
		b[4], b[5] = b[5], b[4]
	}
	return fmt.Sprintf("%02X%02X%02X%02X-%02X%02X-%02X%02X-%02X%02X-%02X%02X%02X%02X%02X%02X",
		b[0], b[1], b[2], b[3], b[4], b[5], b[6], b[7],
		b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15])
}
