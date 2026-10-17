package screenshot

import (
	"fmt"
	"strings"
)

// OCR on-device via Windows.Media.Ocr. O agente não embute um motor de OCR:
// chama o motor que já vem no Windows através de um script PowerShell auxiliar
// (WinRT projetado para .NET com AsTask). Sem dependência nova no go.mod.

// OCRMaxOutputChars limita o texto devolvido ao frontend; uma página inteira de
// texto ainda cabe folgado, e o teto evita payload absurdo de uma imagem hostil.
const OCRMaxOutputChars = 200000

// SanitizeOCRLanguage reduz o idioma a um tag BCP-47 simples (letras, dígitos e
// "-"). É o único dado interpolado além do caminho: precisa ser inofensivo mesmo
// que a UI mande algo como "pt-BR'; Remove-Item ...".
func SanitizeOCRLanguage(language string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(language) {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
		if b.Len() >= 20 {
			break
		}
	}
	return b.String()
}

func psSingleQuote(value string) string {
	// Em single-quoted strings do PowerShell a única escape é duplicar o apóstrofo.
	return strings.ReplaceAll(value, "'", "''")
}

// BuildOCRPowerShellScript monta o script que reconhece o texto da imagem. É
// separado da execução para poder ser testado sem depender do motor do Windows.
//
// O script SEMPRE imprime um JSON de uma linha (ok/engineAvailable/text/error) e
// nunca usa exit code como sinal principal: o binding prefere o JSON e só cai
// para o stderr quando o PowerShell não consegue nem iniciar.
func BuildOCRPowerShellScript(imagePath, language string) string {
	safePath := psSingleQuote(strings.TrimSpace(imagePath))
	safeLanguage := psSingleQuote(SanitizeOCRLanguage(language))
	return fmt.Sprintf(`$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
# Com o stdout redirecionado o host do PowerShell escreve na code page do
# console: texto acentuado do OCR chegava como bytes inválidos e virava U+FFFD
# no JSON. Em host sem console o setter pode falhar, e o OCR não pode morrer
# por causa disso (por isso o try/catch próprio).
try { [Console]::OutputEncoding = [System.Text.Encoding]::UTF8 } catch { }
$imagePath = '%s'
$languageTag = '%s'
function Write-OcrResult([bool]$engineAvailable, [string]$text, [string]$errorText) {
    [ordered]@{ ok = ($errorText -eq ''); engineAvailable = $engineAvailable; text = $text; language = $languageTag; error = $errorText } | ConvertTo-Json -Compress
}
try {
    Add-Type -AssemblyName System.Runtime.WindowsRuntime | Out-Null
    [Windows.Storage.StorageFile,Windows.Foundation,ContentType=WindowsRuntime] | Out-Null
    [Windows.Storage.FileAccessMode,Windows.Foundation,ContentType=WindowsRuntime] | Out-Null
    [Windows.Storage.Streams.IRandomAccessStream,Windows.Foundation,ContentType=WindowsRuntime] | Out-Null
    [Windows.Graphics.Imaging.BitmapDecoder,Windows.Foundation,ContentType=WindowsRuntime] | Out-Null
    [Windows.Graphics.Imaging.SoftwareBitmap,Windows.Foundation,ContentType=WindowsRuntime] | Out-Null
    [Windows.Media.Ocr.OcrEngine,Windows.Foundation,ContentType=WindowsRuntime] | Out-Null
    [Windows.Media.Ocr.OcrResult,Windows.Foundation,ContentType=WindowsRuntime] | Out-Null
    [Windows.Globalization.Language,Windows.Foundation,ContentType=WindowsRuntime] | Out-Null

    $asyncOperationTypeName = 'IAsyncOperation' + [char]96 + '1'
    $asTaskMethod = ([System.WindowsRuntimeSystemExtensions].GetMethods() | Where-Object {
        $_.Name -eq 'AsTask' -and $_.IsGenericMethodDefinition -and
        $_.GetParameters().Count -eq 2 -and
        $_.GetParameters()[0].ParameterType.Name -eq $asyncOperationTypeName -and
        $_.GetParameters()[1].ParameterType -eq [System.Threading.CancellationToken]
    })[0]
    if ($null -eq $asTaskMethod) { throw 'AsTask indisponivel nesta versao do .NET' }

    function Invoke-WinRtAsync([object]$Operation, [Type]$ResultType) {
        $method = $asTaskMethod.MakeGenericMethod($ResultType)
        $task = $method.Invoke($null, @($Operation, [System.Threading.CancellationToken]::None))
        if (-not $task.Wait(20000)) { throw 'tempo esgotado aguardando a operacao WinRT' }
        return $task.Result
    }

    $file = Invoke-WinRtAsync ([Windows.Storage.StorageFile]::GetFileFromPathAsync($imagePath)) ([Windows.Storage.StorageFile])
    $stream = Invoke-WinRtAsync ($file.OpenAsync([Windows.Storage.FileAccessMode]::Read)) ([Windows.Storage.Streams.IRandomAccessStream])
    $decoder = Invoke-WinRtAsync ([Windows.Graphics.Imaging.BitmapDecoder]::CreateAsync($stream)) ([Windows.Graphics.Imaging.BitmapDecoder])
    $bitmap = Invoke-WinRtAsync ($decoder.GetSoftwareBitmapAsync()) ([Windows.Graphics.Imaging.SoftwareBitmap])

    $engine = $null
    if ($languageTag -ne '') {
        try {
            $language = New-Object -TypeName Windows.Globalization.Language -ArgumentList $languageTag
            if ([Windows.Media.Ocr.OcrEngine]::IsLanguageSupported($language)) {
                $engine = [Windows.Media.Ocr.OcrEngine]::TryCreateFromLanguage($language)
            }
        } catch {
            $engine = $null
        }
    }
    if ($null -eq $engine) {
        $engine = [Windows.Media.Ocr.OcrEngine]::TryCreateFromUserProfileLanguages()
    }
    if ($null -eq $engine) {
        throw "motor de OCR do Windows indisponivel (instale o pacote de idioma de OCR; idioma pedido: $languageTag)"
    }

    $result = Invoke-WinRtAsync ($engine.RecognizeAsync($bitmap)) ([Windows.Media.Ocr.OcrResult])
    Write-OcrResult $true ([string]$result.Text) ''
} catch {
    Write-OcrResult $false '' ([string]$_.Exception.Message)
}`, safePath, safeLanguage)
}
