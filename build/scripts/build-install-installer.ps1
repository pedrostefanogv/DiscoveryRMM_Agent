[CmdletBinding()]
param(
    [string]$ProjectRoot = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path,
    [string]$OutputName = "discovery-agent-install.exe",
    [string]$Version = "",
    [string]$DefaultUrl = "",
    [string]$DefaultKey = "",
    [ValidateSet("0", "1")]
    [Alias("DiscoveryEnabled")]
    [string]$AutoProvisioning = "1",
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

function Resolve-WindresPath() {
    $command = Get-Command windres -ErrorAction SilentlyContinue
    if ($command) {
        return $command.Source
    }

    $candidates = @(
        "C:\ProgramData\Chocolatey\lib\mingw\tools\install\mingw64\bin\windres.exe",
        "C:\msys64\mingw64\bin\windres.exe",
        "C:\msys64\usr\bin\windres.exe"
    )

    foreach ($candidate in $candidates) {
        if (Test-Path $candidate) {
            return $candidate
        }
    }

    throw "Comando 'windres' nao encontrado no PATH nem nos locais padrao do MinGW/MSYS2."
}

Assert-Command go
$makensisExe = Resolve-MakensisPath
$windresExe = Resolve-WindresPath

$srcRoot = Join-Path $ProjectRoot "src"
$syncIconsScript = Join-Path $ProjectRoot "build\scripts\sync-icons.ps1"
$binDir = Join-Path $srcRoot "build\bin"
$installerDir = Join-Path $srcRoot "build\windows\installer"
$nsiFile = Join-Path $installerDir "project.nsi"
$agentExe = Join-Path $binDir "discovery-agent.exe"
$iconPath = Join-Path $srcRoot "build\windows\icon.ico"
$sysoPath = Join-Path $srcRoot "resource_windows_amd64.syso"

if (-not (Test-Path $syncIconsScript)) {
    throw "Script de sincronizacao de icones nao encontrado: $syncIconsScript"
}

Write-Output "  Sincronizando icones a partir de build\\*.png..."
& $syncIconsScript -ProjectRoot $ProjectRoot

if (-not (Test-Path $nsiFile)) {
    throw "Arquivo NSIS não encontrado: $nsiFile"
}

if (-not (Test-Path $iconPath)) {
    throw "Icone nao encontrado: $iconPath"
}

if (-not (Test-Path $binDir)) {
    New-Item -ItemType Directory -Path $binDir | Out-Null
}

Write-Output "[1/3] Build do agente (Windows AMD64)..."
Push-Location $srcRoot
try {
    Write-Output "  Gerando recurso de icone (.syso) para o executavel..."
    $rcPath = Join-Path $env:TEMP "discovery_icon.rc"
    $iconPathForRc = ($iconPath -replace '\\', '/')
    Set-Content -Path $rcPath -Value "IDI_APP_ICON ICON `"$iconPathForRc`"" -Encoding ASCII
    & $windresExe --target=pe-x86-64 -i $rcPath -o $sysoPath
    if ($LASTEXITCODE -ne 0 -or -not (Test-Path $sysoPath)) {
        throw "Falha ao gerar recurso de icone com windres"
    }

    $env:CGO_ENABLED = "1"
    $env:GOOS = "windows"
    $env:GOARCH = "amd64"
    $ldflags = @("-H=windowsgui")
    if ($Version -ne "") {
        $ldflags += "-X discovery/app.Version=$Version"
        $ldflags += "-X discovery/internal/buildinfo.Version=$Version"
    }

    go build -tags "desktop,production" -ldflags ($ldflags -join ' ') -o $agentExe .
}
finally {
    if ($rcPath -and (Test-Path $rcPath)) {
        Remove-Item $rcPath -Force -ErrorAction SilentlyContinue
    }
    if ($sysoPath -and (Test-Path $sysoPath)) {
        Remove-Item $sysoPath -Force -ErrorAction SilentlyContinue
    }
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
    "/INPUTCHARSET",
    "UTF8",
    "/DARG_WAILS_AMD64_BINARY=$agentExe",
    "/DARG_OUTFILE_NAME=$OutputName",
    "/DARG_DEFAULT_DISCOVERY=$AutoProvisioning"
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

    # Calcular versao numerica X.X.X.X para VIFileVersion do NSIS (nao aceita pre-release semver)
    # Ex: 1.0.5-beta.1 -> 1.0.5.1 | 1.0.5 -> 1.0.5.0
    $nsisFileVersion = "1.0.0.0"
    if ($Version -match '^(\d+)\.(\d+)\.(\d+)(?:[._-][a-zA-Z]*\.?(\d+))?') {
        $build = if ($Matches[4]) { $Matches[4] } else { "0" }
        $nsisFileVersion = "$($Matches[1]).$($Matches[2]).$($Matches[3]).$build"
    }
    $nsisArgs += "/DINFO_FILEVERSION=$nsisFileVersion"
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
Write-Output "Observacao: o instalador registra o servico Windows, inicia o Discovery e cria a regra de Windows Firewall para permitir a comunicacao do discovery-agent.exe na rede."
