[CmdletBinding()]
param(
    [string]$ProjectRoot = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path,
    [string]$OutputName = "discovery-agent-install.exe",
    [string]$Version = "",
    [string]$DefaultUrl = "",
    [string]$DefaultKey = "",
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

function Resolve-MakensisPath() {
    $command = Get-Command makensis -ErrorAction SilentlyContinue
    if ($command) {
        return $command.Source
    }

    $candidates = @(
        "C:\Program Files (x86)\NSIS\makensis.exe",
        "C:\Program Files\NSIS\makensis.exe"
    )

    foreach ($candidate in $candidates) {
        if (Test-Path $candidate) {
            return $candidate
        }
    }

    throw "Comando 'makensis' nao encontrado no PATH nem nos locais padrao do NSIS."
}

Assert-Command go
$makensisExe = Resolve-MakensisPath

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

# Garantir que o WebView2 bootstrapper existe na pasta tmp/ (necessario pelo macro wails.webview2runtime)
$tmpDir = Join-Path $installerDir "tmp"
$webview2Exe = Join-Path $tmpDir "MicrosoftEdgeWebview2Setup.exe"
if (-not (Test-Path $webview2Exe)) {
    Write-Output "  Baixando WebView2 bootstrapper da Microsoft..."
    if (-not (Test-Path $tmpDir)) {
        New-Item -ItemType Directory -Path $tmpDir | Out-Null
    }
    Invoke-WebRequest -Uri "https://go.microsoft.com/fwlink/p/?LinkId=2124703" -OutFile $webview2Exe -UseBasicParsing
    Write-Output "  WebView2 bootstrapper baixado: $webview2Exe"
}

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
if ($Version -ne "") {
    $nsisArgs += "/DINFO_PRODUCTVERSION=$Version"
}

$nsisArgs += $nsiFile
& $makensisExe @nsisArgs
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
