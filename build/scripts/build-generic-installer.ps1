#Requires -Version 5.1
<#
.SYNOPSIS
    Gera o instalador generico (sem URL/KEY pre-configurados) para Auto-Provisioning.

.DESCRIPTION
    O agente instalado com este pacote nao possui credenciais hard-coded.
    Ao iniciar, ele entra automaticamente em modo Auto-Provisioning:
      - Utiliza mDNS/libp2p para descobrir peers na rede local.
      - Solicita uma oferta de provisionamento ao peer configurado via GET /p2p/config/onboard.
      - Recebe um token temporario, registra-se no servidor e persiste as credenciais.

.NOTES
    Requer: Go 1.21+, NSIS 3.x instalado em C:\Program Files (x86)\NSIS\makensis.exe
    Saida: build\bin\discovery-amd64-installer-generic.exe
#>

param(
    [string]$MakeNSIS = "C:\Program Files (x86)\NSIS\makensis.exe",
    [string]$ProjectRoot = (Split-Path -Parent (Split-Path -Parent $PSScriptRoot))
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$BinaryPath  = Join-Path $ProjectRoot "build\bin\discovery.exe"
$NSIScript   = Join-Path $ProjectRoot "build\windows\installer\project.nsi"
$OutputName  = Join-Path $ProjectRoot "build\bin\discovery-amd64-installer-generic.exe"

Write-Host "`n==> Compilando binario do agente..." -ForegroundColor Cyan
Push-Location $ProjectRoot
try {
    & go build -o $BinaryPath . 2>&1 | Write-Host
    if ($LASTEXITCODE -ne 0) { throw "go build falhou (exit $LASTEXITCODE)" }
} finally {
    Pop-Location
}
Write-Host "    Binario: $BinaryPath" -ForegroundColor Green

Write-Host "`n==> Compilando instalador NSIS (modo generico)..." -ForegroundColor Cyan
if (-not (Test-Path $MakeNSIS)) {
    throw "makensis nao encontrado em: $MakeNSIS`nInstale o NSIS 3.x: https://nsis.sourceforge.io/Download"
}

$relBinary = "..\..\" + (Resolve-Path $BinaryPath -Relative).TrimStart(".\")
& $MakeNSIS `
    "-DARG_WAILS_AMD64_BINARY=$relBinary" `
    "-DARG_DEFAULT_DISCOVERY=1" `
    "-DARG_GENERIC_INSTALL=1" `
    $NSIScript 2>&1 | Write-Host

if ($LASTEXITCODE -ne 0) { throw "makensis falhou (exit $LASTEXITCODE)" }

# Renomear saida padrao para nome generico
$defaultOutput = Join-Path $ProjectRoot "build\bin\discovery-amd64-installer.exe"
if (Test-Path $defaultOutput) {
    Move-Item -Force $defaultOutput $OutputName
    Write-Host "`n==> Instalador generico gerado:" -ForegroundColor Green
    Write-Host "    $OutputName" -ForegroundColor White
} else {
    Write-Warning "Nao foi possivel localizar o arquivo de saida padrao do NSIS para renomear."
}

Write-Host "`nPronto. Distribua '$OutputName' nas novas maquinas." -ForegroundColor Green
Write-Host "O agente entrara em Auto-Provisioning automaticamente ao iniciar na rede local.`n"
