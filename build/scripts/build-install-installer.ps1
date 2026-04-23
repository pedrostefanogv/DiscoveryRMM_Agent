[CmdletBinding()]
param(
    [string]$ProjectRoot = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path,
    [string]$OutputName = "discovery-agent-install.exe",
    [string]$Version = "",
    [string]$DefaultUrl = "",
    [string]$DefaultKey = "",
    [ValidateSet("0", "1")]
    [string]$DiscoveryEnabled = "1",
    [switch]$GenericInstall,
    [switch]$MinimalDefault
)

$ErrorActionPreference = "Stop"

function Assert-Command([string]$Name) {
    if (-not (Get-Command $Name -ErrorAction SilentlyContinue)) {
        throw "Comando '$Name' nao encontrado no PATH."
    }
}

Assert-Command go
Assert-Command makensis

$srcRoot = Join-Path $ProjectRoot "src"
$binDir = Join-Path $srcRoot "build\bin"
$installerDir = Join-Path $srcRoot "build\windows\installer"
$nsiFile = Join-Path $installerDir "project.nsi"
$agentExe = Join-Path $binDir "discovery.exe"

if (-not (Test-Path $nsiFile)) {
    throw "Arquivo NSIS não encontrado: $nsiFile"
}

if (-not (Test-Path $binDir)) {
    New-Item -ItemType Directory -Path $binDir | Out-Null
}

Write-Output "[1/3] Build do agente (Windows AMD64)..."
Push-Location $srcRoot
try {
    $env:CGO_ENABLED = "0"
    $env:GOOS = "windows"
    $env:GOARCH = "amd64"
    $ldflags = @()
    if ($Version -ne "") {
        $ldflags += "-X discovery/app.Version=$Version"
        $ldflags += "-X discovery/internal/buildinfo.Version=$Version"
    }

    if ($ldflags.Count -gt 0) {
        go build -ldflags ($ldflags -join ' ') -o $agentExe .
    }
    else {
        go build -o $agentExe .
    }
}
finally {
    Pop-Location
}

if (-not (Test-Path $agentExe)) {
    throw "Binário do agente não foi gerado: $agentExe"
}

Write-Output "[2/3] Build do instalador padrao (NSIS)..."
$nsisArgs = @(
    "/V3",
    "/DARG_WAILS_AMD64_BINARY=$agentExe",
    "/DARG_OUTFILE_NAME=$OutputName",
    "/DARG_DEFAULT_DISCOVERY=$DiscoveryEnabled"
)

if ($DefaultUrl -ne "") {
    $nsisArgs += "/DARG_DEFAULT_URL=$DefaultUrl"
}
if ($DefaultKey -ne "") {
    $nsisArgs += "/DARG_DEFAULT_KEY=$DefaultKey"
}
if ($GenericInstall) {
    $nsisArgs += "/DARG_GENERIC_INSTALL=1"
}
if ($MinimalDefault) {
    $nsisArgs += "/DARG_DEFAULT_MINIMAL=1"
}
if ($Version -ne "") {
    $nsisArgs += "/DINFO_PRODUCTVERSION=$Version"
}

$nsisArgs += $nsiFile
& makensis @nsisArgs
if ($LASTEXITCODE -ne 0) {
    throw "Falha no makensis (exit code: $LASTEXITCODE)"
}

$installerPath = Join-Path $binDir $OutputName
if (-not (Test-Path $installerPath)) {
    throw "Instalador não encontrado após build: $installerPath"
}

Write-Output "[3/3] Concluido."
Write-Output "Instalador gerado em: $installerPath"
Write-Output "Observacao: o instalador ja registra o servico Windows e inicia o Discovery com discovery/p2p habilitado para operacao em rede local (sujeito ao firewall)."
