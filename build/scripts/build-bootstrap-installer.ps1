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
    [Alias("DiscoveryEnabled")]
    [string]$AutoProvisioning = "1",
    [ValidateSet("0", "1")]
    [string]$EnableWindowsService = "0",
    [string]$ExpectedTag = "",
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

$parsedPayloadUrl = $null
if (-not [Uri]::TryCreate($PayloadUrl, [System.UriKind]::Absolute, [ref]$parsedPayloadUrl)) {
    throw "PayloadUrl inválida: $PayloadUrl"
}

if ($parsedPayloadUrl.Scheme -ne "https") {
    throw "PayloadUrl deve usar HTTPS: $PayloadUrl"
}

if ($ExpectedTag -ne "") {
    $expectedSegment = "/releases/download/$ExpectedTag/"
    if (-not $PayloadUrl.Contains($expectedSegment)) {
        throw "PayloadUrl não corresponde à tag esperada '$ExpectedTag': $PayloadUrl"
    }
}

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

Write-Output "[2/3] Build do bootstrap installer (NSIS)..."
Write-Output "  Payload URL: $PayloadUrl"
if ($ExpectedTag -ne "") {
    Write-Output "  Tag esperada para payload: $ExpectedTag"
}
$nsisArgs = @(
    "/V3",
    "/INPUTCHARSET",
    "UTF8",
    "/DARG_WAILS_AMD64_BINARY=$agentExe",
    "/DARG_BOOTSTRAP_INSTALL=1",
    "/DARG_PAYLOAD_URL=$PayloadUrl",
    "/DARG_PAYLOAD_FILENAME=$PayloadFileName",
    "/DARG_OUTFILE_NAME=$OutputName",
    "/DARG_DEFAULT_DISCOVERY=$AutoProvisioning",
    "/DARG_ENABLE_WINDOWS_SERVICE=$EnableWindowsService"
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
& $makensisExe @nsisArgs
if ($LASTEXITCODE -ne 0) {
    throw "Falha no makensis (exit code: $LASTEXITCODE)"
}

$installerPath = Join-Path $binDir $OutputName
if (-not (Test-Path $installerPath)) {
    throw "Bootstrap installer não encontrado após build: $installerPath"
}

Write-Output "[3/3] Concluido."
Write-Output "Bootstrap gerado em: $installerPath"
if ($EnableWindowsService -eq "1") {
    Write-Output "Esse bootstrap baixa a segunda etapa e executa o instalador completo com servico Windows habilitado."
}
else {
    Write-Output "Esse bootstrap baixa a segunda etapa e executa o instalador completo em modo tray no logon (sem servico Windows)."
}
