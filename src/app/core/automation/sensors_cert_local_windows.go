//go:build windows

package automation

// F6 — Certificado do STORE LOCAL + bindings IIS (extensão do sensor
// CertificateExpiry).
//
// ─────────────────────────────────────────────────────────────────────────────
// POR QUE ESTE ARQUIVO EXISTE (lacuna que ele fecha):
//
//   O coletor existente (sensors_collect_phase5_windows.go:1271) faz
//   tls.DialWithDialer contra um ENDPOINT REMOTO e lê o certificado do
//   handshake. Isso responde "o certificado que ESTE endpoint apresenta vence
//   em N dias" — mas NÃO responde a pergunta que derruba um servidor interno:
//
//     "o certificado que EU instalei na minha máquina, e que o IIS usa no meu
//      site, está vencendo?"
//
//   Uma máquina pode ter um cert vencendo no LocalMachine\My enquanto o
//   endpoint público continua saudável (porque outro nó responde). Antes deste
//   coletor, o agente NÃO enxergava o store local (grep X509Store|certlm = 0).
// ─────────────────────────────────────────────────────────────────────────────
//
// REGRA DE SEGURANÇA:
//   SOMENTE LEITURA. NUNCA exportamos chave privada, nunca alteramos, nunca
//   instalamos/removemos certificado. Lemos apenas os metadados públicos
//   (subject, issuer, thumbprint, validade). Nenhuma credencial é usada — os
//   cmdlets rodam como a identidade do agente (SYSTEM no modo serviço).
//
// Best-effort: cada store é independente; cert store sem permissão de leitura
// (Root/CA exigem elevação em alguns cenários) simplesmente não contribui, e o
// motivo vai em Errors. Nunca panic.

import (
	"context"
	"crypto/sha1"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Contrato de canais (nomes EXATOS que o servidor recebe)
// ─────────────────────────────────────────────────────────────────────────────

const (
	// ChannelCertLocalDaysToExpiry: dias até vencer o certificado que vence
	// PRIMEIRO entre os do store local (o mais urgente — é o que gera alerta).
	ChannelCertLocalDaysToExpiry = "cert_local_days_to_expiry"
	// ChannelCertLocalExpiringCount: quantos certificados do store local vencem
	// dentro do horizonte de atenção.
	ChannelCertLocalExpiringCount = "cert_local_expiring_count"
	// ChannelCertLocalTotalCount: total de certificados lidos no store local.
	ChannelCertLocalTotalCount = "cert_local_total_count"
	// ChannelCertLocalOK: 1 = nenhum certificado no horizonte crítico.
	ChannelCertLocalOK = "cert_local_ok"
	// ChannelCertIISBoundCount: nº de bindings HTTPS do IIS com thumbprint.
	ChannelCertIISBoundCount = "cert_iis_bound_count"
	// ChannelCertIISDaysToExpiry: dias até vencer o certificado IIS mais urgente.
	ChannelCertIISDaysToExpiry = "cert_iis_days_to_expiry"
	// ChannelCertCollectOK: 1 = a coleta conseguiu ler o store, 0 = não leu.
	ChannelCertCollectOK = "cert_collect_ok"
)

// Horizontes de validade (em dias) usados como EVIDÊNCIA. O servidor decide o
// alerta final; estes valores só classificam o que é "urgente" vs "atenção".
const (
	certCriticalDays = 14
	certWarningDays  = 30
	// certMaxCertificates limita quantos certificados processamos por store: um
	// servidor com CA própria pode ter milhares, e o payload não é relatório.
	certMaxCertificates = 300
	// certMaxLocalCertSubject limita o tamanho do subject enviado.
	certMaxLocalCertSubject = 300
)

// localCertificate é um certificado do store local já normalizado.
type localCertificate struct {
	Store         string  `json:"store"`
	Subject       string  `json:"subject,omitempty"`
	Issuer        string  `json:"issuer,omitempty"`
	Thumbprint    string  `json:"thumbprint,omitempty"`
	NotAfter      string  `json:"notAfter,omitempty"`
	DaysToExpiry  float64 `json:"daysToExpiry"`
	HasPrivateKey bool    `json:"hasPrivateKey,omitempty"`
}

// iisBinding é um binding HTTPS do IIS com o thumbprint do certificado usado.
type iisBinding struct {
	Site       string `json:"site,omitempty"`
	Binding    string `json:"binding,omitempty"`
	Thumbprint string `json:"thumbprint,omitempty"`
}

// ─────────────────────────────────────────────────────────────────────────────
// F6.1 — Enumerar o cert store LOCAL
// ─────────────────────────────────────────────────────────────────────────────
//
// FONTE: cmdlet Get-ChildItem do provider Cert: (PSDrive). É a forma suportada
// de enumerar SEM P/Invoke e SEM certutil: o provider já está presente em
// qualquer PowerShell, e Get-ChildItem expõe os metadados públicos.
//
// USAMOS Get-ChildItem E NÃO [System.Security.Cryptography.X509Certificates.
// X509Store]: a classe .NET exige que o Windows PowerShell carregue o assembly
// na sessão, o que é lento e frágil em Server Core; o provider Cert: é builtin.
//
// Stores lidos: LocalMachine\My (pessoal — onde vivem os certs de IIS/RDP) e,
// quando a leitura permitir, Root e CA (autoridades — um root vencendo quebra a
// cadeia inteira, então também vale alertar).
//
// $_.NotAfter é DateTime; convertemos para epoch (unix ms) NO POWERSHELL porque
// formato de data localizado quebraria o parse em SO não-inglês.

const certLocalStoreScriptTemplate = `$ErrorActionPreference = 'SilentlyContinue'
$stores = @('My','Root','CA')
$out = @()
foreach ($s in $stores) {
  $path = "Cert:\LocalMachine\$s"
  if (-not (Test-Path $path)) { continue }
  Get-ChildItem -Path $path | Select-Object -First %d | ForEach-Object {
    [PSCustomObject]@{
      Store      = $s
      Subject    = $_.Subject
      Issuer     = $_.Issuer
      Thumbprint = $_.Thumbprint
      NotAfter   = $_.NotAfter.ToUniversalTime().ToString('o')
      HasPrivateKey = [bool]$_.HasPrivateKey
    }
  } | ForEach-Object { $out += $_ }
}
$out | ConvertTo-Json -Compress -Depth 3`

// certStoreRow é a linha crua lida do PowerShell antes da normalização.
type certStoreRow struct {
	Store         string `json:"Store"`
	Subject       string `json:"Subject"`
	Issuer        string `json:"Issuer"`
	Thumbprint    string `json:"Thumbprint"`
	NotAfter      string `json:"NotAfter"`
	HasPrivateKey bool   `json:"HasPrivateKey"`
}

// parseLocalCertStoreJSON converte a saída JSON do provider Cert: na lista de
// certificados normalizada. É PURA (não executa nada), recebendo o "agora" como
// parâmetro para que o cálculo de dias restantes seja determinístico no teste.
//
// Trata array, objeto único e saída vazia. Certs sem NotAfter legível são
// mantidos com DaysToExpiry 0? NÃO: são descartados — um cert sem validade
// legível não permite decidir nada, e publicar 0 criaria alerta falso.
func parseLocalCertStoreJSON(raw string, now time.Time) []localCertificate {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "[]" || trimmed == "null" {
		return nil
	}

	var rows []certStoreRow
	if err := json.Unmarshal([]byte(trimmed), &rows); err != nil {
		var single certStoreRow
		if err2 := json.Unmarshal([]byte(trimmed), &single); err2 != nil {
			return nil
		}
		rows = []certStoreRow{single}
	}

	certs := make([]localCertificate, 0, len(rows))
	for _, row := range rows {
		notAfter, err := time.Parse(time.RFC3339, strings.TrimSpace(row.NotAfter))
		if err != nil {
			// Formato "o" do .NET pode trazer 7 casas decimais na fração;
			// tentamos também o layout com precisão fixa antes de desistir.
			notAfter, err = time.Parse("2006-01-02T15:04:05.9999999Z07:00", strings.TrimSpace(row.NotAfter))
			if err != nil {
				continue
			}
		}

		days := notAfter.Sub(now).Hours() / 24
		certs = append(certs, localCertificate{
			Store:         strings.TrimSpace(row.Store),
			Subject:       truncarAD(strings.TrimSpace(row.Subject), certMaxLocalCertSubject),
			Issuer:        truncarAD(strings.TrimSpace(row.Issuer), certMaxLocalCertSubject),
			Thumbprint:    normalizarThumbprint(row.Thumbprint),
			NotAfter:      notAfter.UTC().Format(time.RFC3339),
			DaysToExpiry:  sensorRound2(days),
			HasPrivateKey: row.HasPrivateKey,
		})

		if len(certs) >= certMaxCertificates {
			break
		}
	}

	// Ordena pelo que vence primeiro: o mais urgente fica na frente e o
	// primeiro item é o que o servidor deve olhar.
	sort.SliceStable(certs, func(i, j int) bool {
		return certs[i].DaysToExpiry < certs[j].DaysToExpiry
	})

	return certs
}

// normalizarThumbprint remove espaços e o caractere invisível que o PowerShell
// frequentemente prefixa em strings de certificado (U+200E LEFT-TO-RIGHT MARK).
// Sem isso, o thumbprint reportado não casaria com o do binding do IIS e o
// cruzamento binding<->certificado falharia silenciosamente.
func normalizarThumbprint(raw string) string {
	limpo := strings.Map(func(r rune) rune {
		switch r {
		case '\u200e', '\u200f', ' ', '\t', '\n', '\r':
			return -1
		}
		return r
	}, raw)
	return strings.ToUpper(limpo)
}

// ─────────────────────────────────────────────────────────────────────────────
// F6.2 — Bindings HTTPS do IIS
// ─────────────────────────────────────────────────────────────────────────────

const iisBindingScript = `$ErrorActionPreference = 'SilentlyContinue'
Import-Module WebAdministration -ErrorAction SilentlyContinue
if (-not (Get-Command Get-WebBinding -ErrorAction SilentlyContinue)) { exit 0 }
$out = @()
Get-WebBinding | Where-Object { $_.protocol -eq 'https' } | ForEach-Object {
  $thumb = $null
  try {
    $cert = Get-Item ("Cert:\LocalMachine\My\" + $_.certificateHash) -ErrorAction SilentlyContinue
    if ($cert) { $thumb = $cert.Thumbprint }
  } catch { }
  if (-not $thumb) { $thumb = $_.certificateHash }
  [PSCustomObject]@{
    Site       = $_.ItemXPath
    Binding    = $_.bindingInformation
    Thumbprint = $thumb
  }
}
$out | ConvertTo-Json -Compress -Depth 3`

// iisBindingRow é a linha crua de binding do IIS.
type iisBindingRow struct {
	Site       string `json:"Site"`
	Binding    string `json:"Binding"`
	Thumbprint string `json:"Thumbprint"`
}

// parseIISBindingsJSON converte a saída do Get-WebBinding. É PURA.
//
// Bindings sem thumbprint são descartados: sem o certificado não há o que
// validar, e uma linha vazia só polui a evidência.
func parseIISBindingsJSON(raw string) []iisBinding {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "[]" || trimmed == "null" {
		return nil
	}

	var rows []iisBindingRow
	if err := json.Unmarshal([]byte(trimmed), &rows); err != nil {
		var single iisBindingRow
		if err2 := json.Unmarshal([]byte(trimmed), &single); err2 != nil {
			return nil
		}
		rows = []iisBindingRow{single}
	}

	bindings := make([]iisBinding, 0, len(rows))
	for _, row := range rows {
		thumb := normalizarThumbprint(row.Thumbprint)
		if thumb == "" {
			continue
		}
		bindings = append(bindings, iisBinding{
			Site:       truncarAD(limparItemXPath(row.Site), 200),
			Binding:    truncarAD(strings.TrimSpace(row.Binding), 200),
			Thumbprint: thumb,
		})
	}
	return bindings
}

// limparItemXPath extrai um nome legível do ItemXPath do IIS, que vem no
// formato "/system.applicationHost/sites/site[@name='Default Web Site']".
// Sem limpar, a UI mostraria o XPath inteiro em vez do nome do site.
func limparItemXPath(xpath string) string {
	texto := strings.TrimSpace(xpath)
	if texto == "" {
		return ""
	}
	const marcador = "@name='"
	if idx := strings.Index(texto, marcador); idx >= 0 {
		resto := texto[idx+len(marcador):]
		if fim := strings.Index(resto, "'"); fim >= 0 {
			return strings.TrimSpace(resto[:fim])
		}
	}
	return texto
}

// ─────────────────────────────────────────────────────────────────────────────
// F6.3 — Coleta
// ─────────────────────────────────────────────────────────────────────────────

// alvoEhCertStoreLocal decide se o sensor de certificado aponta para o STORE
// LOCAL em vez de um endpoint remoto. É PURA.
//
// A decisão exige um marcador EXPLÍCITO para não mudar o comportamento de quem
// já tem sensores configurados: qualquer valor que pareça host/IP/porta
// continua indo para o caminho remoto. Aceitamos as formas que um técnico
// digitaria naturalmente para dizer "aqui nesta máquina".
func alvoEhCertStoreLocal(target string) bool {
	switch strings.ToLower(strings.TrimSpace(target)) {
	case "", "local", "localmachine", "-", ".", "self", "maquina", "máquina":
		return true
	default:
		return false
	}
}

// collectLocalCertificateSample lê o store local e (quando houver IIS) os
// bindings HTTPS, e devolve a evidência agregada.
//
// Roda em QUALQUER Windows (não só Server): um desktop com IIS Express ou um
// certificado de RDP vencendo também se beneficia. Diferente do F5, não há
// ganho em restringir por ProductType.
//
// Best-effort: se o store não puder ser lido, Errors registra o motivo e o
// canal cert_collect_ok sai 0 — o servidor então sabe que "não leu", em vez de
// interpretar ausência de certificados como "está tudo certo".
func collectLocalCertificateSample(ctx context.Context, cfg *SensorConfig) sensorCollection {
	evidence := certLocalEvidence{}

	// Store local.
	script := strings.Replace(certLocalStoreScriptTemplate, "%d", itoaLimit(certMaxCertificates), 1)
	if saida, err := runSensorPowerShell(ctx, minDuracao(sensorPowershellTimeout, cfg), script); err == nil {
		evidence.StoreRead = true
		agora := time.Now()
		evidence.Certificates = parseLocalCertStoreJSON(saida, agora)
	} else {
		evidence.Errors = append(evidence.Errors, "cert store indisponivel: "+err.Error())
	}

	// Bindings IIS (opcional: só existe onde o IIS está instalado).
	if saida, err := runSensorPowerShell(ctx, minDuracao(sensorPowershellTimeout, cfg), iisBindingScript); err == nil {
		evidence.IISBindings = parseIISBindingsJSON(saida)
	}
	// Falha do IIS NÃO entra em Errors: "não tem IIS" é o caso normal em
	// milhares de máquinas, e não é falha de coleta.

	return buildLocalCertificateCollection(evidence, time.Now())
}

// certLocalEvidence é a evidência crua do store local + IIS.
type certLocalEvidence struct {
	StoreRead    bool
	Certificates []localCertificate
	IISBindings  []iisBinding
	Errors       []string
}

// buildLocalCertificateCollection converte a evidência em canais + mensagem.
// É PURA (o "agora" entra por parâmetro) para ser testável sem tocar no sistema.
func buildLocalCertificateCollection(evidence certLocalEvidence, now time.Time) sensorCollection {
	channels := make(map[string]float64, 8)
	var partes []string

	channels[ChannelCertCollectOK] = boolToFloat(evidence.StoreRead)

	if evidence.StoreRead {
		channels[ChannelCertLocalTotalCount] = float64(len(evidence.Certificates))

		// Dias até vencer o MAIS URGENTE. Este é o sinal que gera alerta: um
		// servidor com 200 certificados saudáveis e 1 vencendo em 3 dias precisa
		// que o canal primário aponte os 3 dias, não a média.
		if len(evidence.Certificates) > 0 {
			maisUrgente := evidence.Certificates[0] // já ordenado por dias asc
			channels[ChannelCertLocalDaysToExpiry] = maisUrgente.DaysToExpiry
			channels[ChannelCertLocalOK] = boolToFloat(maisUrgente.DaysToExpiry > certCriticalDays)

			vencendo := 0
			for _, cert := range evidence.Certificates {
				if cert.DaysToExpiry <= certWarningDays {
					vencendo++
				}
			}
			channels[ChannelCertLocalExpiringCount] = float64(vencendo)

			partes = append(partes, "certificado mais urgente: "+
				formatarDias(maisUrgente.DaysToExpiry)+" dia(s)")
			if maisUrgente.Subject != "" {
				partes = append(partes, "("+maisUrgente.Subject+")")
			}
			if loc := localDoCertificado(maisUrgente); loc != "" {
				partes = append(partes, "em LocalMachine\\"+loc)
			}
			if vencendo > 0 {
				partes = append(partes, itoaLimit(vencendo)+" certificado(s) vencendo em ate "+
					itoaLimit(certWarningDays)+" dias")
			}

			// Evidência dos mais urgentes (limitada): o técnico precisa saber
			// QUAIS vão quebrar, não só quantos.
			for i, cert := range evidence.Certificates {
				if i >= 5 {
					break
				}
				partes = append(partes, cert.Subject+" ["+cert.Store+"] vence em "+
					formatarDias(cert.DaysToExpiry)+"d")
			}
		} else {
			partes = append(partes, "nenhum certificado no store local")
		}
	}

	if len(evidence.IISBindings) > 0 {
		channels[ChannelCertIISBoundCount] = float64(len(evidence.IISBindings))

		// Cruza cada binding com o certificado do store para achar o mais urgente
		// entre os USADOS pelo IIS. Um binding órfão (thumbprint que não está no
		// store) é um problema por si só e é reportado como tal.
		porThumbprint := make(map[string]localCertificate, len(evidence.Certificates))
		for _, cert := range evidence.Certificates {
			porThumbprint[cert.Thumbprint] = cert
		}

		orfaos := 0
		var maisUrgenteIIS *localCertificate
		for _, binding := range evidence.IISBindings {
			cert, ok := porThumbprint[binding.Thumbprint]
			if !ok {
				orfaos++
				continue
			}
			if maisUrgenteIIS == nil || cert.DaysToExpiry < maisUrgenteIIS.DaysToExpiry {
				copia := cert
				maisUrgenteIIS = &copia
			}
		}

		if maisUrgenteIIS != nil {
			channels[ChannelCertIISDaysToExpiry] = maisUrgenteIIS.DaysToExpiry
			partes = append(partes, "IIS: "+itoaLimit(len(evidence.IISBindings))+" binding(s) HTTPS, "+
				"o mais urgente vence em "+formatarDias(maisUrgenteIIS.DaysToExpiry)+" dia(s)")
		} else {
			partes = append(partes, "IIS: "+itoaLimit(len(evidence.IISBindings))+" binding(s) HTTPS")
		}
		if orfaos > 0 {
			partes = append(partes, itoaLimit(orfaos)+" binding(s) apontando para certificado ausente do store")
		}
	}

	if len(evidence.Errors) > 0 {
		partes = append(partes, "falhas de coleta: "+strings.Join(evidence.Errors, "; "))
	}

	collection := sensorCollection{Channels: channels}
	if len(partes) > 0 {
		collection.Measure.ServiceState = sensorString(truncarAD(strings.Join(partes, " | "), 2000))
	}
	return collection
}

// localDoCertificado devolve o nome amigável do store.
func localDoCertificado(cert localCertificate) string {
	switch strings.ToLower(cert.Store) {
	case "my":
		return "My"
	case "root":
		return "Root"
	case "ca":
		return "CA"
	default:
		return cert.Store
	}
}

// formatarDias formata dias restantes com 1 casa; valores negativos indicam
// certificado JÁ VENCIDO e são preservados como tal (o sinal é a evidência).
func formatarDias(dias float64) string {
	return strconv.FormatFloat(dias, 'f', 1, 64)
}

// itoaLimit converte int em string (evita depender de strconv no cabeçalho).
func itoaLimit(v int) string {
	return strconv.Itoa(v)
}

// ─────────────────────────────────────────────────────────────────────────────
// Utilitário de parsing de X.509 para o caminho nativo (sem PowerShell).
// ─────────────────────────────────────────────────────────────────────────────

// certificadoDoDER extrai os metadados públicos de um certificado DER.
//
// Existe como caminho alternativo/suplementar: se algum dia a leitura via
// provider Cert: for bloqueada por política, o mesmo resultado pode sair de um
// handle do store via crypto/x509. Hoje é usada pelos testes para validar que a
// normalização bate com a biblioteca padrão.
//
// SOMENTE LEITURA: não acessa chave privada (RawSubjectPublicKeyInfo é público).
func certificadoDoDER(der []byte, store string, now time.Time) (localCertificate, bool) {
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		return localCertificate{}, false
	}
	days := parsed.NotAfter.Sub(now).Hours() / 24
	return localCertificate{
		Store:        store,
		Subject:      truncarAD(parsed.Subject.String(), certMaxLocalCertSubject),
		Issuer:       truncarAD(parsed.Issuer.String(), certMaxLocalCertSubject),
		Thumbprint:   normalizarThumbprint(hexDoThumbprint(parsed.Raw)),
		NotAfter:     parsed.NotAfter.UTC().Format(time.RFC3339),
		DaysToExpiry: sensorRound2(days),
	}, true
}

// hexDoThumbprint converte o SHA-1 do certificado (o mesmo que o Windows chama
// de thumbprint) em hex maiúsculo, sem separadores.
//
// SHA-1 aqui NÃO é escolha de segurança: é o identificador que o próprio
// Windows usa como "thumbprint" de certificado. Usamos a biblioteca padrão em
// vez de reimplementar a codificação hex.
func hexDoThumbprint(raw []byte) string {
	sum := sha1.Sum(raw) //nolint:gosec // thumbprint do Windows, nao assinatura
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}
