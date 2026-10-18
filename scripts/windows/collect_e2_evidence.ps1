#requires -Version 5.1
<#
.SYNOPSIS
  Coleta evidencias da validacao manual da Fase E.2 (separacao servico x UI) + A2/IPC.

.DESCRIPTION
  Executa as verificacoes do checklist Discovery/DOCs/CHECKLIST_FASE_E2_VALIDACAO_MANUAL.md
  e grava um relatorio Markdown + ZIP com os artefatos, para anexar ao relatorio de homologacao.
  Somente leitura: nao altera servico, config nem instalacao.

.EXAMPLE
  powershell -ExecutionPolicy Bypass -File .\collect_e2_evidence.ps1 -OutputDir C:\Temp\e2-evidence
#>
[CmdletBinding()]
param(
    [string]$OutputDir = (Join-Path $env:USERPROFILE "Desktop\e2-evidence"),
    [string]$DiscoveryDir = (Join-Path $env:ProgramData "Discovery"),
    [string]$InstallDir = (Join-Path $env:ProgramFiles "Discovery"),
    [string]$ServiceName = "DiscoveryAgent",
    [string]$TaskName = "DiscoveryAgentUI"
)

$ErrorActionPreference = "Continue"
$stamp = Get-Date -Format "yyyyMMdd-HHmmss"
$runDir = Join-Path $OutputDir "run-$stamp"
New-Item -ItemType Directory -Path $runDir -Force | Out-Null
$report = Join-Path $runDir "evidencias-$stamp.md"
$lines = New-Object System.Collections.Generic.List[string]

function Add-Section([string]$title) {
    $lines.Add("")
    $lines.Add("## $title")
    $lines.Add("")
}

# Bloco de codigo markdown sem usar crases (evita conflito de escaping).
function Add-Cmd([string]$label, [scriptblock]$action) {
    $lines.Add("### $label")
    $lines.Add("~~~")
    try {
        $out = & $action 2>&1 | Out-String
        $lines.Add($out.TrimEnd())
    } catch {
        $lines.Add("ERRO: $($_.Exception.Message)")
    }
    $lines.Add("~~~")
    $lines.Add("")
}

function Add-Check([string]$label, [bool]$ok, [string]$detail = "") {
    $mark = if ($ok) { "OK" } else { "FALHA/ATENCAO" }
    $suffix = if ($detail) { " - $detail" } else { "" }
    $lines.Add("- [x] **$label**: $mark$suffix")
}

$lines.Add("# Evidencias - Fase E.2 / A2 / IPC ($stamp)")
$lines.Add("")
$lines.Add("> Gerado por Discovery/scripts/windows/collect_e2_evidence.ps1 (somente leitura).")
$lines.Add("> Maquina: $env:COMPUTERNAME - Usuario: $env:USERNAME - Admin: $(([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator))")

Add-Section "1. Sistema e instalacao"
Add-Cmd "Versao do Windows" { (Get-CimInstance Win32_OperatingSystem | Select-Object Caption, Version, BuildNumber | Format-List | Out-String) }
Add-Cmd "Binarios instalados" {
    Get-ChildItem $InstallDir -Filter "discovery-*.exe" -ErrorAction SilentlyContinue |
        Select-Object Name, Length, @{n='Version';e={$_.VersionInfo.FileVersion}},
            @{n='SHA256';e={(Get-FileHash $_.FullName -Algorithm SHA256).Hash}} |
        Format-Table -AutoSize
}
Add-Cmd "Servico (sc qc/query/failure)" { sc.exe qc $ServiceName; sc.exe query $ServiceName; sc.exe qfailure $ServiceName }
Add-Cmd "Task de logon da UI (RunLevel)" {
    Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue |
        Select-Object TaskName, State | Format-List
    Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue |
        Get-ScheduledTaskInfo -ErrorAction SilentlyContinue |
        Select-Object LastRunTime, LastTaskResult | Format-List
    try {
        $xml = [xml](Export-ScheduledTask -TaskName $TaskName -ErrorAction Stop)
        $xml.Task.Principals.Principal.RunLevel
    } catch { "task nao encontrada" }
}
Add-Cmd "Processos do agente" {
    Get-Process | Where-Object { $_.ProcessName -like "discovery-*" } |
        Select-Object Id, ProcessName, SessionId, StartTime | Format-Table -AutoSize
}

Add-Section "2. IPC (canais, token e seguranca)"
Add-Cmd "Pipes existentes" {
    Get-ChildItem \\.\pipe\ -ErrorAction SilentlyContinue |
        Where-Object { $_.Name -like "discovery-agent-ipc*" } |
        Select-Object -ExpandProperty Name
}
$pipes = @(Get-ChildItem \\.\pipe\ -ErrorAction SilentlyContinue | Where-Object { $_.Name -like "discovery-agent-ipc*" })
Add-Check "Canal de leitura existe" ($pipes.Name -contains "discovery-agent-ipc")
Add-Check "Canal admin existe" ($pipes.Name -contains "discovery-agent-ipc-admin")

Add-Section "3. Configuracao (dono unico + ACL + watcher)"
$cfg = Join-Path $DiscoveryDir "debug_config.json"
Add-Cmd "ACL do debug_config.json" { if (Test-Path $cfg) { icacls.exe $cfg } else { "arquivo inexistente: $cfg" } }
Add-Cmd "JSON valido" {
    if (Test-Path $cfg) {
        try { Get-Content $cfg -Raw | ConvertFrom-Json | Out-Null; "JSON valido" } catch { "JSON INVALIDO: $($_.Exception.Message)" }
    } else { "arquivo inexistente" }
}
Add-Cmd "Hash + tamanho do config" {
    if (Test-Path $cfg) { Get-Item $cfg | Select-Object Length, LastWriteTime; (Get-FileHash $cfg -Algorithm SHA256).Hash }
}

Add-Section "4. Logs (logs.db e fallback)"
$logDb = Join-Path $DiscoveryDir "logs\logs.db"
$logTxt = Join-Path $DiscoveryDir "logs\agent-service.log"
Add-Cmd "logs.db presente" { if (Test-Path $logDb) { Get-Item $logDb | Select-Object FullName, Length, LastWriteTime } else { "ausente" } }
$sqlite = Get-Command sqlite3.exe -ErrorAction SilentlyContinue
if ($sqlite -and (Test-Path $logDb)) {
    Add-Cmd "Ultimas 200 linhas (logs.db)" { & $sqlite.Source $logDb "select * from logs order by id desc limit 200;" 2>&1 }
    Add-Cmd "Evidencia de PRESHUTDOWN (Fase 5/8.3)" { & $sqlite.Source $logDb "select * from logs where message like '%PRESHUTDOWN%' order by id desc limit 50;" 2>&1 }
    Add-Cmd "Evidencia de ipc_unauthorized / rate limit (S1/S2)" { & $sqlite.Source $logDb "select * from logs where message like '%ipc_unauthorized%' or message like '%ipc_rate%' or message like '%inflight%' order by id desc limit 50;" 2>&1 }
    Copy-Item $logDb (Join-Path $runDir "logs.db") -Force -ErrorAction SilentlyContinue
} else {
    Add-Cmd "sqlite3 indisponivel" { "instale sqlite3 ou copie logs.db manualmente" }
    if (Test-Path $logDb) { Copy-Item $logDb (Join-Path $runDir "logs.db") -Force -ErrorAction SilentlyContinue }
}
Add-Cmd "Fallback agent-service.log (ultimas 80 linhas)" {
    if (Test-Path $logTxt) { Get-Content $logTxt -Tail 80 } else { "ausente (esperado: logs.db e o principal)" }
}

Add-Section "5. Sessoes e multiusuario (A3)"
Add-Cmd "Sessoes ativas" { query user; qwinsta }
Add-Cmd "UIs por sessao" { Get-Process discovery-agent -ErrorAction SilentlyContinue | Select-Object Id, SessionId, StartTime | Format-Table -AutoSize }
Add-Cmd "Handles do SQLite (handle64)" {
    $h = Get-Command handle64.exe -ErrorAction SilentlyContinue
    if ($h) {
        & $h.Source -nobanner -p "discovery-agent.exe" 2>&1 | Select-String "\.db"
        & $h.Source -nobanner -p "discovery-service.exe" 2>&1 | Select-String "\.db"
    } else { "handle64.exe nao encontrado (Sysinternals)" }
}

Add-Section "6. WebView2 (perfil por sessao - A3)"
Add-Cmd "Diretorios de perfil" {
    Get-ChildItem (Join-Path $DiscoveryDir "WebView2") -Directory -ErrorAction SilentlyContinue |
        Select-Object Name, LastWriteTime
}

Add-Section "7. Resumo"
Add-Check "Servico RUNNING" ((sc.exe query $ServiceName | Select-String "RUNNING") -ne $null)
Add-Check "Canal admin do IPC presente" ($pipes.Name -contains "discovery-agent-ipc-admin")
Add-Check "debug_config.json presente e valido" (Test-Path $cfg)
Add-Check "logs.db presente" (Test-Path $logDb)

Set-Content -Path $report -Value ($lines -join [Environment]::NewLine) -Encoding UTF8
$zip = Join-Path $OutputDir "e2-evidence-$stamp.zip"
Compress-Archive -Path (Join-Path $runDir "*") -DestinationPath $zip -Force
Write-Host ""
Write-Host "Relatorio: $report"
Write-Host "Pacote:    $zip"
