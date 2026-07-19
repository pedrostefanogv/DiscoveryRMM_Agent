# Repair-MojibakeFiles.ps1
# Corrige arquivos com duplo encoding UTF-8 (mojibake) e remove BOM.
# Uso: .\Repair-MojibakeFiles.ps1 [-DryRun]
[CmdletBinding()]
param(
    [switch]$DryRun
)

$ErrorActionPreference = "Stop"

# Lista de arquivos afetados pela revisão de 2026-07-19
$files = @(
    "src\app\agent_config.go",
    "src\app\app_test.go",
    "src\app\p2p_libp2p_transport.go",
    "src\app\p2p_onboarding.go",
    "src\app\remote_debug.go",
    "src\app\remote_debug_test.go",
    "src\internal\agentconn\runtime_nats.go",
    "src\internal\automation\service.go",
    "src\internal\selfupdate\updater.go",
    "src\main.go",
    "src\build\windows\installer\project.nsi",
    "src\frontend\partials\views\psadtView.html",
    "build\scripts\build-bootstrap-installer.ps1",
    "build\scripts\build-install-installer.ps1"
)

$root = Resolve-Path (Join-Path $PSScriptRoot "..")

# Mapeamento de sequencias mojibake conhecidas para caracteres corretos.
# As chaves abaixo sao bytes UTF-8 duplo-encodados que aparecem como mojibake.
# Usamos [char] para evitar problemas de encoding no proprio script.
$emDash  = [char]0x2014   # -
$enDash  = [char]0x2013   # -
$ldquo   = [char]0x201C   # "
$rdquo   = [char]0x201D   # "
$lsquo   = [char]0x2018   # '
$rsquo   = [char]0x2019   # '
$bullet  = [char]0x2022   # *
$hellip  = "..."
$replacements = [ordered]@{
    ([char]0xE2 + [char]0x80 + [char]0x94) = $emDash   # mojibake de em-dash
    ([char]0xE2 + [char]0x80 + [char]0x9C) = $ldquo    # mojibake de left double quote
    ([char]0xE2 + [char]0x80 + [char]0x9D) = $rdquo    # mojibake de right double quote
    ([char]0xE2 + [char]0x80 + [char]0x98) = $lsquo    # mojibake de left single quote
    ([char]0xE2 + [char]0x80 + [char]0x99) = $rsquo    # mojibake de right single quote
    ([char]0xE2 + [char]0x80 + [char]0xA6) = $hellip   # mojibake de ellipsis
    ([char]0xE2 + [char]0x80 + [char]0xA2) = $bullet   # mojibake de bullet
    ([char]0xE2 + [char]0x80 + [char]0x93) = $enDash   # mojibake de en-dash
    [char]0xC2                          = ""           # byte residual nao-ASCII (prefixo de mojibake)
    ([char]0xFFFD.ToString() + "?")     = $emDash      # replacement char + ? (em-dash perdido)
}

function Repair-File {
    param([string]$Path)

    if (-not (Test-Path $Path)) {
        Write-Warning "Arquivo não encontrado: $Path"
        return $false
    }

    $bytes = [System.IO.File]::ReadAllBytes($Path)
    if ($bytes.Length -eq 0) {
        Write-Warning "Arquivo vazio: $Path"
        return $false
    }

    $hadBom = ($bytes.Length -ge 3 -and $bytes[0] -eq 0xEF -and $bytes[1] -eq 0xBB -and $bytes[2] -eq 0xBF)
    $noBom = if ($hadBom) { $bytes[3..($bytes.Length - 1)] } else { $bytes }

    # Decodifica bytes como UTF-8 (Go exige UTF-8 válido).
    $text = [System.Text.Encoding]::UTF8.GetString($noBom)

    # Detecta mojibake antes de tentar reparar.
    $hadMojibake = $false
    foreach ($key in $replacements.Keys) {
        if ($text.Contains($key)) { $hadMojibake = $true; break }
    }
    if ($text -match [char]0xFFFD) { $hadMojibake = $true }

    if (-not $hadBom -and -not $hadMojibake) {
        Write-Host "OK (sem alteração): $Path"
        return $false
    }

    # Aplica substituições de mojibake.
    $fixed = $text
    foreach ($key in $replacements.Keys) {
        if ($key) {
            $fixed = $fixed.Replace($key, $replacements[$key])
        }
    }
    # Remove replacement chars (U+FFFD) — bytes perdidos no duplo encoding.
    $fixed = $fixed -replace [char]0xFFFD, ''

    $fixedBytes = [System.Text.Encoding]::UTF8.GetBytes($fixed)

    if ($DryRun) {
        Write-Host "DRY-RUN: $Path (BOM=$hadBom, mojibake=$hadMojibake)"
        return $true
    }

    [System.IO.File]::WriteAllBytes($Path, $fixedBytes)
    Write-Host "REPARADO: $Path (BOM removido=$hadBom, mojibake corrigido=$hadMojibake)"
    return $true
}

$changed = 0
foreach ($rel in $files) {
    $abs = Join-Path $root $rel
    if (Repair-File -Path $abs) { $changed++ }
}

Write-Host ""
Write-Host "Total de arquivos reparados: $changed de $($files.Count)"
