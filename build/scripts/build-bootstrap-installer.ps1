[CmdletBinding()]
param(
    [string]$ProjectRoot = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path,
    [Parameter(Mandatory = $true)]
    [string]$PayloadUrl,
    [string]$Version = "",
    [string]$PayloadSha256 = "",
    [string]$PayloadFileName = "discovery-stage2-installer.exe",
    [string]$OutputName = "discovery-agent-bootstrap.exe",
    [ValidateSet("0", "1")]
    [string]$DiscoveryEnabled = "1",
    [switch]$GenericInstall
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

Write-Output "[2/3] Build do bootstrap installer (NSIS)..."
$nsisArgs = @(
    "/V3",
    "/DARG_WAILS_AMD64_BINARY=$agentExe",
    "/DARG_BOOTSTRAP_INSTALL=1",
    "/DARG_PAYLOAD_URL=$PayloadUrl",
    "/DARG_PAYLOAD_FILENAME=$PayloadFileName",
    "/DARG_OUTFILE_NAME=$OutputName",
    "/DARG_DEFAULT_DISCOVERY=$DiscoveryEnabled"
)

if ($PayloadSha256 -ne "") {
    $nsisArgs += "/DARG_PAYLOAD_SHA256=$PayloadSha256"
}
if ($GenericInstall) {
    $nsisArgs += "/DARG_GENERIC_INSTALL=1"
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
    throw "Bootstrap installer não encontrado após build: $installerPath"
}

Write-Output "[3/3] Concluido."
Write-Output "Bootstrap gerado em: $installerPath"
Write-Output "Esse bootstrap baixa a segunda etapa e executa o instalador completo com Discovery habilitado por padrao."
