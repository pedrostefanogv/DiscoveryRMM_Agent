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
    [string]$ExpectedTag = "",
    [switch]$GenericInstall
)

$ErrorActionPreference = "Stop"

# Auto-detect version from wails.json productVersion if not explicitly provided
if ($Version -eq "") {
    $wailsJsonPath = Join-Path $ProjectRoot "src\wails.json"
    if (Test-Path $wailsJsonPath) {
        $wailsConfig = Get-Content $wailsJsonPath -Raw | ConvertFrom-Json
        if ($wailsConfig.info.productVersion) {
            $Version = $wailsConfig.info.productVersion
            Write-Output "Versao detectada do wails.json: $Version"
        }
    }
    if ($Version -eq "") {
        Write-Warning "Versao nao definida e nao detectada do wails.json; binario ficara com buildinfo.Version='0.0.0' (self-update loop pode ocorrer)"
    }
}

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
    throw "PayloadUrl invÃ¡lida: $PayloadUrl"
}

if ($parsedPayloadUrl.Scheme -ne "https") {
    throw "PayloadUrl deve usar HTTPS: $PayloadUrl"
}

if ($ExpectedTag -ne "") {
    $expectedSegment = "/releases/download/$ExpectedTag/"
    if (-not $PayloadUrl.Contains($expectedSegment)) {
        throw "PayloadUrl nÃ£o corresponde Ã  tag esperada '$ExpectedTag': $PayloadUrl"
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
    throw "Arquivo NSIS nÃ£o encontrado: $nsiFile"
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

    # Build Windows AMD64 (go build direto — Wails v3 embeda o frontend via //go:embed).
    $ldflags = @()
    if ($Version -ne "") {
        $ldflags += "-X discovery/app.Version=$Version"
        $ldflags += "-X discovery/internal/buildinfo.Version=$Version"
    } else {
        Write-Warning "BUILD SEM VERSAO: -ldflags sem -X discovery/internal/buildinfo.Version — o binario ficara com '0.0.0' (self-update pode entrar em loop)"
    }
    # Injeta commit hash do git para decisao de self-update (version+commit)
    $gitCommit = (git -C $srcRoot rev-parse --short=8 HEAD 2>$null)
    if ($gitCommit) {
        $ldflags += "-X discovery/internal/buildinfo.Commit=$gitCommit"
        Write-Output "  buildinfo.Commit = $gitCommit"
    } else {
        Write-Warning "  git rev-parse falhou; buildinfo.Commit ficara 'unknown'"
    }

    # Wails v3: o frontend é embedado via //go:embed all:frontend e os bindings
    # já estão gerados em frontend/bindings. Usamos `go build` direto para ter
    # controle total sobre -o e -ldflags (injeção de versão/commit).
    $goBuildArgs = @("build", "-tags", "desktop,production", "-o", $agentExe)
    if ($ldflags.Count -gt 0) {
        $goBuildArgs += "-ldflags"
        $goBuildArgs += ($ldflags -join ' ')
    }
    & go @goBuildArgs
    if ($LASTEXITCODE -ne 0) {
        throw "Falha no go build (exit code: $LASTEXITCODE)"
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
    throw "BinÃ¡rio do agente nÃ£o foi gerado: $agentExe"
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
    "/DARG_DEFAULT_DISCOVERY=$AutoProvisioning"
)

if ($PayloadSha256 -ne "") {
    $nsisArgs += "/DARG_PAYLOAD_SHA256=$PayloadSha256"
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
    throw "Bootstrap installer nÃ£o encontrado apÃ³s build: $installerPath"
}

Write-Output "[3/3] Concluido."
Write-Output "Bootstrap gerado em: $installerPath"
Write-Output "Esse bootstrap baixa a segunda etapa e executa o instalador completo em modo tray no logon (Task Scheduler, sem servico Windows)."

