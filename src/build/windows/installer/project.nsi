Unicode true

####
## Please note: Template replacements don't work in this file. They are provided with default defines like
## mentioned underneath.
## If the keyword is not defined, "wails_tools.nsh" will populate them with the values from ProjectInfo.
## If they are defined here, "wails_tools.nsh" will not touch them. This allows to use this project.nsi manually
## from outside of Wails for debugging and development of the installer.
##
## For development first make a wails nsis build to populate the "wails_tools.nsh":
## > wails build --target windows/amd64 --nsis
## Then you can call makensis on this file with specifying the path to your binary:
## For a AMD64 only installer:
## > makensis -DARG_WAILS_AMD64_BINARY=..\..\bin\app.exe
## For a ARM64 only installer:
## > makensis -DARG_WAILS_ARM64_BINARY=..\..\bin\app.exe
## For a installer with both architectures:
## > makensis -DARG_WAILS_AMD64_BINARY=..\..\bin\app-amd64.exe -DARG_WAILS_ARM64_BINARY=..\..\bin\app-arm64.exe
##
## Build-time defaults for installer configuration (optional):
## > makensis ... -DARG_DEFAULT_URL=api.example.com -DARG_DEFAULT_KEY=key_xpto -DARG_DEFAULT_DISCOVERY=1 -DARG_DEFAULT_MINIMAL=1
####
## The following information is taken from the ProjectInfo file, but they can be overwritten here.
####
## !define INFO_PROJECTNAME    "MyProject" # Default "{{.Name}}"
## !define INFO_COMPANYNAME    "MyCompany" # Default "{{.Info.CompanyName}}"
## !define INFO_PRODUCTNAME    "MyProduct" # Default "{{.Info.ProductName}}"
## !define INFO_PRODUCTVERSION "1.0.0"     # Default "{{.Info.ProductVersion}}"
## !define INFO_COPYRIGHT      "Copyright" # Default "{{.Info.Copyright}}"
###
## !define PRODUCT_EXECUTABLE  "Application.exe"      # Default "${INFO_PROJECTNAME}.exe"
## !define UNINST_KEY_NAME     "UninstKeyInRegistry"  # Default "${INFO_COMPANYNAME}${INFO_PRODUCTNAME}"
!define INFO_PROJECTNAME    "discovery"
!define INFO_COMPANYNAME    "Discovery"
!define INFO_PRODUCTNAME    "Discovery"
!ifndef INFO_PRODUCTVERSION
  !tempfile DISCOVERY_VER_INC
  # Wails v3: lê a versão de build/config.yml (info.version).
  # Fallback legado: src/wails.json (v2: info.productVersion) — removido na migração v3.
  !system 'powershell -NoProfile -Command "$cfg = Join-Path (Split-Path ''${__FILE__}'') ''..\..\..\build\config.yml''; if (Test-Path $cfg) { $c = Get-Content $cfg -Raw; if ($c -match ''(?ms)^\s*info:\s*\n(?:.*\n)*?\s+version:\s*[\"'']?([^\"''\s#]+)'') { Set-Content ''${DISCOVERY_VER_INC}'' -Value (''!define INFO_PRODUCTVERSION '' + $Matches[1]) -Enc ASCII } }"'
  !searchparse /file "${DISCOVERY_VER_INC}" "" INFO_PRODUCTVERSION
  !delfile "${DISCOVERY_VER_INC}"
  !undef DISCOVERY_VER_INC
  !ifndef INFO_PRODUCTVERSION
    !define INFO_PRODUCTVERSION "1.0.0"
  !endif
!endif
!ifndef INFO_FILEVERSION
!define INFO_FILEVERSION "${INFO_PRODUCTVERSION}.0"
!endif
!define INFO_COPYRIGHT      "Copyright (c) 2026 Discovery"
!define PRODUCT_EXECUTABLE  "discovery-agent.exe"
!define SERVICE_EXECUTABLE  "discovery-service.exe"
!define LEGACY_PRODUCT_EXECUTABLE "discovery.exe"
!define UNINST_KEY_NAME     "Discovery.RMM"
!define DISCOVERY_UI_TASK_NAME "DiscoveryAgentUI"
!define DISCOVERY_SERVICE_NAME "DiscoveryAgent"
!define DISCOVERY_FIREWALL_RULE_NAME "Discovery Agent Network Access"
; O core P2P (TransferServer + host libp2p) roda no SERVICO, nao na UI
; (discovery-service.exe -> RunServiceMode -> runCoreStartup -> P2PCoord.Run).
; Regra program-scoped vale por caminho de executavel: sem esta regra o
; Windows bloqueia todo trafego de entrada do P2P (peers nao conectam).
!define DISCOVERY_SERVICE_FIREWALL_RULE_NAME "Discovery Agent Service P2P"
; Regra por porta (defesa em profundidade): faixa HTTP P2P + porta fixa libp2p.
!define DISCOVERY_P2P_PORTS_RULE_NAME "Discovery Agent P2P Ports"
!define DISCOVERY_P2P_PORTS_UDP_RULE_NAME "Discovery Agent P2P Ports UDP"
!define DISCOVERY_P2P_HTTP_PORT_RANGE "41080-41120"
!define DISCOVERY_P2P_LIBP2P_PORT "41121"

!ifndef ARG_WAILS_AMD64_BINARY
!ifdef WAILS_AMD64_BINARY
!define ARG_WAILS_AMD64_BINARY "${WAILS_AMD64_BINARY}"
!endif
!endif

# ── Serviço Windows embutido no payload (obrigatório por padrão) ──
# BUILD_HAS_SERVICE=1 quando o makensis recebeu o binário do serviço
# (ARG_SERVICE_AMD64_BINARY / ARG_SERVICE_ARM64_BINARY). O `File` correspondente
# fica AQUI no project.nsi, e não no wails_tools.nsh auto-gerado, para que uma
# regeneração do template (`wails3 build`) não remova o serviço do payload.
#
# Build sem o define FALHA em tempo de compilação: um instalador sem
# discovery-service.exe instala a UI em modo standalone, loga "serviço ausente"
# e reporta "Instalacao concluida com sucesso" — falha silenciosa já observada
# em campo (agente sem serviço Windows). Escape hatch explícito para builds
# deliberadamente somente-UI: /DALLOW_UI_ONLY_BUILD=1.
!ifdef ARG_SERVICE_AMD64_BINARY
!define BUILD_HAS_SERVICE "1"
!else
!ifdef ARG_SERVICE_ARM64_BINARY
!define BUILD_HAS_SERVICE "1"
!endif
!endif
!ifndef BUILD_HAS_SERVICE
!ifndef ALLOW_UI_ONLY_BUILD
!error "Discovery: ARG_SERVICE_AMD64_BINARY/ARG_SERVICE_ARM64_BINARY nao definido. O instalador sairia SEM discovery-service.exe (agente sem servico Windows). Passe /DARG_SERVICE_AMD64_BINARY=<caminho do discovery-service.exe> ou, para build explicitamente somente-UI, /DALLOW_UI_ONLY_BUILD=1."
!endif
!define BUILD_HAS_SERVICE "0"
!endif

## Build-time defaults (can be overridden by install-time CLI parameters)
!ifdef ARG_DEFAULT_URL
!define BUILD_DEFAULT_URL "${ARG_DEFAULT_URL}"
!else
!ifdef DEFAULT_URL
!define BUILD_DEFAULT_URL "${DEFAULT_URL}"
!else
!define BUILD_DEFAULT_URL ""
!endif
!endif

!ifdef ARG_DEFAULT_KEY
!define BUILD_DEFAULT_KEY "${ARG_DEFAULT_KEY}"
!else
!ifdef DEFAULT_KEY
!define BUILD_DEFAULT_KEY "${DEFAULT_KEY}"
!else
!define BUILD_DEFAULT_KEY ""
!endif
!endif

!ifdef ARG_DEFAULT_DISCOVERY
!define BUILD_DEFAULT_DISCOVERY "${ARG_DEFAULT_DISCOVERY}"
!else
!ifdef DEFAULT_DISCOVERY
!define BUILD_DEFAULT_DISCOVERY "${DEFAULT_DISCOVERY}"
!else
!define BUILD_DEFAULT_DISCOVERY "1"
!endif
!endif

!ifdef ARG_DEFAULT_MINIMAL
!define BUILD_DEFAULT_MINIMAL "${ARG_DEFAULT_MINIMAL}"
!else
!ifdef DEFAULT_MINIMAL
!define BUILD_DEFAULT_MINIMAL "${DEFAULT_MINIMAL}"
!else
!define BUILD_DEFAULT_MINIMAL "0"
!endif
!endif

!ifdef ARG_GENERIC_INSTALL
!define BUILD_GENERIC_INSTALL "1"
!else
!ifdef GENERIC_INSTALL
!define BUILD_GENERIC_INSTALL "1"
!else
!define BUILD_GENERIC_INSTALL "0"
!endif
!endif

!ifdef ARG_UPDATE_INSTALL
!define BUILD_UPDATE_INSTALL "1"
!else
!ifdef UPDATE_INSTALL
!define BUILD_UPDATE_INSTALL "1"
!else
!define BUILD_UPDATE_INSTALL "0"
!endif
!endif

!ifdef ARG_BOOTSTRAP_INSTALL
!define BUILD_BOOTSTRAP_INSTALL "1"
!else
!ifdef BOOTSTRAP_INSTALL
!define BUILD_BOOTSTRAP_INSTALL "1"
!else
!define BUILD_BOOTSTRAP_INSTALL "0"
!endif
!endif

!ifdef ARG_PAYLOAD_URL
!define BUILD_PAYLOAD_URL "${ARG_PAYLOAD_URL}"
!else
!ifdef PAYLOAD_URL
!define BUILD_PAYLOAD_URL "${PAYLOAD_URL}"
!else
!define BUILD_PAYLOAD_URL ""
!endif
!endif

!ifdef ARG_PAYLOAD_SHA256
!define BUILD_PAYLOAD_SHA256 "${ARG_PAYLOAD_SHA256}"
!else
!ifdef PAYLOAD_SHA256
!define BUILD_PAYLOAD_SHA256 "${PAYLOAD_SHA256}"
!else
!define BUILD_PAYLOAD_SHA256 ""
!endif
!endif

!ifdef ARG_PAYLOAD_FILENAME
!define BUILD_PAYLOAD_FILENAME "${ARG_PAYLOAD_FILENAME}"
!else
!ifdef PAYLOAD_FILENAME
!define BUILD_PAYLOAD_FILENAME "${PAYLOAD_FILENAME}"
!else
!define BUILD_PAYLOAD_FILENAME "discovery-stage2-installer.exe"
!endif
!endif

!ifdef ARG_OUTFILE_NAME
!define BUILD_OUTFILE_NAME "${ARG_OUTFILE_NAME}"
!else
!ifdef OUTFILE_NAME
!define BUILD_OUTFILE_NAME "${OUTFILE_NAME}"
!else
!define BUILD_OUTFILE_NAME "${INFO_PROJECTNAME}-${ARCH}-installer.exe"
!endif
!endif
####
## !define REQUEST_EXECUTION_LEVEL "admin"            # Default "admin"  see also https://nsis.sourceforge.io/Docs/Chapter4.html
####
## Include the wails tools
####
!include "wails_tools.nsh"

# The version information for this two must consist of 4 parts
VIProductVersion "${INFO_FILEVERSION}"
VIFileVersion    "${INFO_FILEVERSION}"

VIAddVersionKey "CompanyName"     "${INFO_COMPANYNAME}"
VIAddVersionKey "FileDescription" "${INFO_PRODUCTNAME} Installer"
VIAddVersionKey "ProductVersion"  "${INFO_PRODUCTVERSION}"
VIAddVersionKey "FileVersion"     "${INFO_PRODUCTVERSION}"
VIAddVersionKey "LegalCopyright"  "${INFO_COPYRIGHT}"
VIAddVersionKey "ProductName"     "${INFO_PRODUCTNAME}"

# Enable HiDPI support. https://nsis.sourceforge.io/Reference/ManifestDPIAware
ManifestDPIAware true

!include "MUI.nsh"
!include "nsDialogs.nsh"
!include "LogicLib.nsh"
!include "FileFunc.nsh"
!include "WordFunc.nsh"

# ── Macros de logging do instalador ──
# Loga mensagens com timestamp em %ProgramData%\Discovery\logs\installer.log
# Uso: ${InstallerLogInit}, ${InstallerLog} "msg", ${InstallerLogError} "msg"
Var InstallerLogFile

!macro _InstallerLogInit
   ReadEnvStr $R0 "ProgramData"
   ${If} $R0 != ""
      CreateDirectory "$R0\Discovery\logs"
      StrCpy $InstallerLogFile "$R0\Discovery\logs\installer.log"
      ${GetTime} "" "L" $0 $1 $2 $3 $4 $5 $6
      FileOpen $R9 "$InstallerLogFile" a
      ${If} $R9 != ""
         FileWrite $R9 "===== Discovery Agent Installer Log =====$\r$\n"
         FileWrite $R9 "Date: $2/$1/$0 $4:$5:$6$\r$\n"
         FileWrite $R9 "Version: ${INFO_PRODUCTVERSION}$\r$\n"
         FileWrite $R9 "InstallDir: $INSTDIR$\r$\n"
         # A13: $CMDLINE pode conter /KEY=<chave do servidor> — nunca gravar
         # em texto plano no installer.log (diretório acessível a Users).
         # Logamos as flags estruturadas (sem segredos) para diagnóstico.
         FileWrite $R9 "CommandLine: (oculta — A13: nunca logar /KEY em claro)$\r$\n"
         FileWrite $R9 "Flags: URL='$ServerUrl' AP='$AutoProvisioning' MINIMAL='$MinimalMode' UPDATE='$UpdateMode' GENERIC='$GenericMode' SERVICE='${BUILD_HAS_SERVICE}' KEY=<oculta>$\r$\n"
         FileWrite $R9 "=========================================$\r$\n"
         FileClose $R9
      ${EndIf}
   ${EndIf}
!macroend
!define InstallerLogInit "!insertmacro _InstallerLogInit"

!macro _InstallerLog _MSG
   ${If} $InstallerLogFile != ""
      ${GetTime} "" "L" $0 $1 $2 $3 $4 $5 $6
      FileOpen $R9 "$InstallerLogFile" a
      ${If} $R9 != ""
         FileWrite $R9 "$2-$1-$0 $4:$5:$6 ${_MSG}$\r$\n"
         FileClose $R9
      ${EndIf}
   ${EndIf}
   DetailPrint "${_MSG}"
!macroend
!define InstallerLog "!insertmacro _InstallerLog"

!macro _InstallerLogError _MSG
   ${If} $InstallerLogFile != ""
      ${GetTime} "" "L" $0 $1 $2 $3 $4 $5 $6
      FileOpen $R9 "$InstallerLogFile" a
      ${If} $R9 != ""
         FileWrite $R9 "$2-$1-$0 $4:$5:$6 [ERROR] ${_MSG}$\r$\n"
         FileClose $R9
      ${EndIf}
   ${EndIf}
   DetailPrint "[ERROR] ${_MSG}"
!macroend
!define InstallerLogError "!insertmacro _InstallerLogError"

!define MUI_ICON "..\icon.ico"
!define MUI_UNICON "..\icon.ico"
# !define MUI_WELCOMEFINISHPAGE_BITMAP "resources\leftimage.bmp" #Include this to add a bitmap on the left side of the Welcome Page. Must be a size of 164x314
!define MUI_FINISHPAGE_NOAUTOCLOSE # Wait on the INSTFILES page so the user can take a look into the details of the installation steps
!define MUI_FINISHPAGE_RUN
!define MUI_FINISHPAGE_RUN_TEXT "Abrir ${INFO_PRODUCTNAME} agora"
!define MUI_FINISHPAGE_RUN_FUNCTION LaunchInstalledApp
!define MUI_ABORTWARNING # This will warn the user if they exit from the installer.

# Variáveis para armazenar as configurações
Var Dialog
Var UrlLabel
Var UrlText
Var KeyLabel
Var KeyText
Var DiscoveryCheck

Var ServerUrl
Var ServerKey
Var AutoProvisioning
Var AllowInsecureTls
Var MinimalMode
Var GenericMode
Var UpdateMode
Var PayloadUrl
Var PayloadSha256
Var PayloadFileName

; Campos canonicos do agent (derivados de ServerUrl)
Var ApiServer
Var ApiInsecure
# Rollback do serviço no self-update (item 2): "1" indica que o binário do
# serviço foi renomeado para .bak_update nesta execução do instalador.
Var ServiceBackupCreated
# "1" indica que o health check falhou e o serviço foi revertido.
Var ServiceRollbackDone
# "1" indica que algum binário não pôde ser substituído (arquivo em uso após
# as tentativas). Nesse caso o instalador NÃO pode reportar sucesso: o updater
# usa log/exit code para decidir retry e o circuit breaker.
Var UpdateIncomplete

!insertmacro MUI_PAGE_WELCOME # Welcome to the installer page.
# !insertmacro MUI_PAGE_LICENSE "resources\eula.txt" # Adds a EULA page to the installer
!insertmacro MUI_PAGE_DIRECTORY # In which folder install page.

# Página customizada para configurações do agente
Page custom AgentConfigPage AgentConfigPageLeave

!insertmacro MUI_PAGE_INSTFILES # Installing page.
!insertmacro MUI_PAGE_FINISH # Finished installation page.

!insertmacro MUI_UNPAGE_INSTFILES # Uinstalling page

!insertmacro MUI_LANGUAGE "English" # Set the Language of the installer

# Importar macros para parsing de linha de comando
!insertmacro GetParameters
!insertmacro GetOptions

## The following two statements can be used to sign the installer and the uninstaller. The path to the binaries are provided in %1
#!uninstfinalize 'signtool --file "%1"'
#!finalize 'signtool --file "%1"'

Name "${INFO_PRODUCTNAME}"
OutFile "..\..\bin\${BUILD_OUTFILE_NAME}" # Name of the installer's file.
InstallDir "$PROGRAMFILES64\${INFO_PRODUCTNAME}" # Default installing folder ($PROGRAMFILES is Program Files folder).
InstallDirRegKey HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${UNINST_KEY_NAME}" "InstallLocation"
ShowInstDetails show # This will always show the installation details.

Function .onInit
   # Estado do rollback do serviço (self-update): zerado a cada execução.
   StrCpy $ServiceBackupCreated "0"
   StrCpy $ServiceRollbackDone "0"
   StrCpy $UpdateIncomplete "0"

   !insertmacro wails.checkArchitecture

   # ── Help (/?  /H  /HELP) ──
   # Deve vir ANTES de SetSilent e StrCpy de defaults para funcionar
   # mesmo em builds que forcam silent (UPDATE_INSTALL, BOOTSTRAP_STAGE2).
   # REGRA: nao misturar prefixos (/ com -- ou -). Se detectar mistura, ignora
   # flags de help e segue instalacao normal.
   ${GetParameters} $R0
   ${GetOptions} $R0 "/?" $R1
   ${IfNot} ${Errors}
      Call ShowHelpMessageBox
      Abort
   ${EndIf}
   ${GetOptions} $R0 "/H" $R1
   ${IfNot} ${Errors}
      Call ShowHelpMessageBox
      Abort
   ${EndIf}
   ${GetOptions} $R0 "/HELP" $R1
   ${IfNot} ${Errors}
      Call ShowHelpMessageBox
      Abort
   ${EndIf}

   # Definir valores padrão vindos do build
   StrCpy $ServerUrl "${BUILD_DEFAULT_URL}"
   StrCpy $ServerKey "${BUILD_DEFAULT_KEY}"
   StrCpy $AutoProvisioning "${BUILD_DEFAULT_DISCOVERY}"
   StrCpy $AllowInsecureTls ""
   StrCpy $MinimalMode "${BUILD_DEFAULT_MINIMAL}"
   StrCpy $GenericMode "${BUILD_GENERIC_INSTALL}"
   StrCpy $UpdateMode "0"
   StrCpy $PayloadUrl "${BUILD_PAYLOAD_URL}"
   StrCpy $PayloadSha256 "${BUILD_PAYLOAD_SHA256}"
   StrCpy $PayloadFileName "${BUILD_PAYLOAD_FILENAME}"

   # Build de update: modo silencioso por padrão e sem wizard.
   ${If} "${BUILD_UPDATE_INSTALL}" == "1"
      StrCpy $UpdateMode "1"
      StrCpy $MinimalMode "1"
      SetSilent silent

      # Em update, preservar pasta da instalacao existente para evitar migracao de path.
      ReadRegStr $R2 HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${UNINST_KEY_NAME}" "InstallLocation"
      ${If} $R2 != ""
         StrCpy $INSTDIR $R2
      ${EndIf}
   ${EndIf}

   # Normalizar defaults inválidos
   ${If} $AutoProvisioning != "0"
   ${AndIf} $AutoProvisioning != "1"
      StrCpy $AutoProvisioning "1"
   ${EndIf}

   ${If} $MinimalMode != "0"
   ${AndIf} $MinimalMode != "1"
      StrCpy $MinimalMode "0"
   ${EndIf}

   # Obter parametros da linha de comando
   ${GetParameters} $R0

   # Parse UPDATE (atualizacao in-place): silencioso, sem wizard e sem sobrescrever config.
   # Short form: /U
   ${GetOptions} $R0 "/UPDATE" $R1
   ${IfNot} ${Errors}
      StrCpy $UpdateMode "1"
      StrCpy $MinimalMode "1"
      SetSilent silent
      ReadRegStr $R2 HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${UNINST_KEY_NAME}" "InstallLocation"
      ${If} $R2 != ""
         StrCpy $INSTDIR $R2
      ${EndIf}
   ${EndIf}
   ${GetOptions} $R0 "/U" $R1
   ${IfNot} ${Errors}
      StrCpy $UpdateMode "1"
      StrCpy $MinimalMode "1"
      SetSilent silent
      ReadRegStr $R2 HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${UNINST_KEY_NAME}" "InstallLocation"
      ${If} $R2 != ""
         StrCpy $INSTDIR $R2
      ${EndIf}
   ${EndIf}

   # Parse URL
   ${GetOptions} $R0 "/URL=" $R1
   ${If} $R1 != ""
      StrCpy $ServerUrl $R1
   ${EndIf}

   # Parse KEY
   # Short form: /K=
   ${GetOptions} $R0 "/KEY=" $R1
   ${If} $R1 != ""
      StrCpy $ServerKey $R1
   ${EndIf}
   ${GetOptions} $R0 "/K=" $R1
   ${If} $R1 != ""
      StrCpy $ServerKey $R1
   ${EndIf}

   # Parse PAYLOAD_URL (override de build para bootstrap)
   # Short form: /PU=
   ${GetOptions} $R0 "/PAYLOAD_URL=" $R1
   ${If} $R1 != ""
      StrCpy $PayloadUrl $R1
   ${EndIf}
   ${GetOptions} $R0 "/PU=" $R1
   ${If} $R1 != ""
      StrCpy $PayloadUrl $R1
   ${EndIf}

   # A12: PAYLOAD_URL (override de runtime) aceita APENAS https:// —
   # "bootstrap.exe /PU=http://evil/x.exe" baixava e executava como admin.
   ${If} $PayloadUrl != ""
      StrCpy $R2 "$PayloadUrl" 8
      ${If} $R2 != "https://"
         MessageBox MB_ICONSTOP "PAYLOAD_URL invalida: apenas https:// e aceito."
         Abort
      ${EndIf}
   ${EndIf}

   # Parse AUTO_PROVISIONING (0 ou 1) - chave canonica para zero-touch provisioning.
   # Aceita /DISCOVERY= como alias legado para compatibilidade com automacoes antigas.
   # Short form: /AP=
   ${GetOptions} $R0 "/AUTO_PROVISIONING=" $R1
   ${If} $R1 != ""
      ${If} $R1 == "0"
      ${OrIf} $R1 == "1"
         StrCpy $AutoProvisioning $R1
      ${EndIf}
   ${EndIf}
   ${GetOptions} $R0 "/AP=" $R1
   ${If} $R1 != ""
      ${If} $R1 == "0"
      ${OrIf} $R1 == "1"
         StrCpy $AutoProvisioning $R1
      ${EndIf}
   ${EndIf}
   ${GetOptions} $R0 "/DISCOVERY=" $R1
   ${If} $R1 != ""
      ${If} $R1 == "0"
      ${OrIf} $R1 == "1"
         StrCpy $AutoProvisioning $R1
      ${EndIf}
   ${EndIf}

   # Parse ALLOW_INSECURE_TLS (1/true/yes/on habilita persistencia no config)
   # Short form: /TLS=
   ${GetOptions} $R0 "/ALLOW_INSECURE_TLS=" $R1
   ${If} $R1 != ""
      ${If} $R1 == "1"
      ${OrIf} $R1 == "true"
      ${OrIf} $R1 == "TRUE"
      ${OrIf} $R1 == "yes"
      ${OrIf} $R1 == "YES"
      ${OrIf} $R1 == "on"
      ${OrIf} $R1 == "ON"
         StrCpy $AllowInsecureTls "1"
      ${EndIf}
   ${EndIf}
   ${GetOptions} $R0 "/TLS=" $R1
   ${If} $R1 != ""
      ${If} $R1 == "1"
      ${OrIf} $R1 == "true"
      ${OrIf} $R1 == "TRUE"
      ${OrIf} $R1 == "yes"
      ${OrIf} $R1 == "YES"
      ${OrIf} $R1 == "on"
      ${OrIf} $R1 == "ON"
         StrCpy $AllowInsecureTls "1"
      ${EndIf}
   ${EndIf}
   # Flag /BOOTSTRAP_STAGE2: chamado internamente pelo bootstrapper.
   # Forca silencio absoluto (sem janela, sem progresso, sem wizard) para
   # evitar duas telas de instalacao sobrepostas (bootstrap + stage2).
   # Deve ser processado ANTES do /MINIMAL para ter precedencia.
   ${GetOptions} $R0 "/BOOTSTRAP_STAGE2" $R1
   ${IfNot} ${Errors}
      SetSilent silent
      StrCpy $MinimalMode "1"
   ${EndIf}
   # Verificar modo minimo
   # Short form: /M
   ${GetOptions} $R0 "/MINIMAL" $R1
   ${IfNot} ${Errors}
      StrCpy $MinimalMode "1"
      # Em modo minimo, pular wizard se tiver parametros
      ${If} $ServerUrl != ""
      ${AndIf} $ServerKey != ""
         SetSilent normal  # Mostra so o progresso
      ${EndIf}
   ${EndIf}
   # Short form: /M
   ${GetOptions} $R0 "/M" $R1
   ${IfNot} ${Errors}
      StrCpy $MinimalMode "1"
      # Em modo mínimo, pular wizard se tiver parâmetros
      ${If} $ServerUrl != ""
      ${AndIf} $ServerKey != ""
         SetSilent normal  # Mostra só o progresso
      ${EndIf}
   ${EndIf}

   # Modo genérico: sem URL/KEY — agente entra em auto-provisioning via P2P
   # Short form: /G
   ${GetOptions} $R0 "/GENERIC" $R1
   ${IfNot} ${Errors}
      StrCpy $GenericMode "1"
   ${EndIf}
   ${GetOptions} $R0 "/G" $R1
   ${IfNot} ${Errors}
      StrCpy $GenericMode "1"
   ${EndIf}
FunctionEnd

# .onInstFailed: callback do NSIS quando a instalação aborta. É o bloco de
# recuperação do item 2: se o abort aconteceu DEPOIS do sc stop (falha de
# cópia, cancelamento, erro em Section), o serviço não pode ficar parado.
Function .onInstFailed
   # Primeiro devolve os binários renomeados para .bak_update e que ainda não
   # foram substituídos (abort/cancelamento entre o backup e a cópia). Sem isso
   # o serviço (ou a UI) fica SEM binário no $INSTDIR e o sc start falha.
   Call RecoverAgentBinariesIfMissing
   Call RecoverAgentServiceIfStopped
FunctionEnd

# Função para criar a página de configuração do agente
Function AgentConfigPage
   # Modo genérico: sem wizard, agente entra em auto-provisioning via P2P
   ${If} $GenericMode == "1"
      Abort
   ${EndIf}

   # Pular a página se estiver em modo silencioso ou mínimo
   ${If} ${Silent}
   ${OrIf} $MinimalMode == "1"
      Abort
   ${EndIf}

   # Se já temos URL e KEY via CLI, pular o wizard
   ${If} $ServerUrl != ""
   ${AndIf} $ServerKey != ""
      Abort
   ${EndIf}

   nsDialogs::Create 1018
   Pop $Dialog
   ${If} $Dialog == error
      Abort
   ${EndIf}

   # Título da página
   !insertmacro MUI_HEADER_TEXT "Configuração do Agente Discovery" "Configure a conexão com o servidor"

   # Label e campo de texto para URL
   ${NSD_CreateLabel} 0 10u 100% 12u "URL do Servidor:"
   Pop $UrlLabel
   ${NSD_CreateText} 0 25u 100% 12u $ServerUrl
   Pop $UrlText

   # Label e campo de texto para KEY
   ${NSD_CreateLabel} 0 50u 100% 12u "Chave de API:"
   Pop $KeyLabel
   ${NSD_CreateText} 0 65u 100% 12u $ServerKey
   Pop $KeyText

   # Checkbox para habilitar/desabilitar Auto-Provisioning (zero-touch)
   ${NSD_CreateCheckbox} 0 90u 100% 12u "Habilitar Auto-Provisioning (zero-touch via P2P)"
   Pop $DiscoveryCheck
   ${If} $AutoProvisioning == "1"
      ${NSD_Check} $DiscoveryCheck
   ${EndIf}

   nsDialogs::Show
FunctionEnd

# Função para capturar os valores da página customizada
Function AgentConfigPageLeave
   ${NSD_GetText} $UrlText $ServerUrl
   ${NSD_GetText} $KeyText $ServerKey

   ${NSD_GetState} $DiscoveryCheck $R0
   ${If} $R0 == ${BST_CHECKED}
      StrCpy $AutoProvisioning "1"
   ${Else}
      StrCpy $AutoProvisioning "0"
   ${EndIf}
FunctionEnd

Section
    !insertmacro wails.setShellContext

      SetOutPath $INSTDIR

      # ── Logging do instalador ──
      ${InstallerLogInit}

      !if "${BUILD_BOOTSTRAP_INSTALL}" == "1"
         ${InstallerLog} "Modo bootstrapper detectado"
         # Bootstrapper: grava config local e dispara instalador completo de segunda etapa.
         Call EnsureSharedDataDir
         Call DeriveApiFields
         Call SaveAgentConfig
         Call DownloadAndRunStage2
      !else
         ${InstallerLog} "Modo install/update detectado (updateMode=$UpdateMode minimal=$MinimalMode)"
         ${InstallerLog} "InstallDir: $INSTDIR"

         # Atualizacao in-place: parar instancias anteriores para evitar lock no executavel.
         Call PrepareForInPlaceUpdate

         # Garantir sobrescrita forcada dos binarios (default do NSIS e on,
         # mas explicitamos para clareza e para nao pular em caso de erro).
         SetOverwrite on
         AllowSkipFiles off

         ${InstallerLog} "Iniciando copia de arquivos (SetOverwrite=on AllowSkipFiles=off)..."

         !insertmacro wails.webview2runtime
         !insertmacro wails.files

         ; Binário do serviço: embutido AQUI e não no wails_tools.nsh (que é
         ; auto-gerado por `wails build` e perde patches manuais a cada
         ; regeneração). Sem esta cópia o $INSTDIR fica sem
         ; discovery-service.exe e o RegisterAgentService cai em standalone.
         !if "${BUILD_HAS_SERVICE}" == "1"
            !ifdef ARG_SERVICE_AMD64_BINARY
               ${If} ${IsNativeAMD64}
                  File "/oname=${SERVICE_EXECUTABLE}" "${ARG_SERVICE_AMD64_BINARY}"
               ${EndIf}
            !endif
            !ifdef ARG_SERVICE_ARM64_BINARY
               ${If} ${IsNativeARM64}
                  File "/oname=${SERVICE_EXECUTABLE}" "${ARG_SERVICE_ARM64_BINARY}"
               ${EndIf}
            !endif
         !endif

         ${IfNot} ${Errors}
            ${InstallerLog} "wails.files concluido com sucesso"
         ${Else}
            ${InstallerLogError} "wails.files falhou — binario pode nao ter sido copiado"
         ${EndIf}

         ; Verificar se o binario foi realmente copiado
         IfFileExists "$INSTDIR\${PRODUCT_EXECUTABLE}" 0 install_binary_missing
         ${InstallerLog} "Binario verificado: $INSTDIR\${PRODUCT_EXECUTABLE} existe"
         ; Cleanup do .bak_update — o rename no PrepareForInPlaceUpdate
         ; liberou o path original, agora o novo binário está no lugar.
         Delete "$INSTDIR\${PRODUCT_EXECUTABLE}.bak_update"
         ${InstallerLog} "Cleanup: .bak_update removido"
         Goto install_binary_ok
         install_binary_missing:
         ${InstallerLogError} "Binario NAO encontrado apos copia: $INSTDIR\${PRODUCT_EXECUTABLE}"
         ; Se a cópia falhou, tentar restaurar o .bak. Sem backup (instalação
         ; nova) o app simplesmente não existe: NÃO pode sair como sucesso.
         IfFileExists "$INSTDIR\${PRODUCT_EXECUTABLE}.bak_update" 0 install_binary_no_backup
         Rename "$INSTDIR\${PRODUCT_EXECUTABLE}.bak_update" "$INSTDIR\${PRODUCT_EXECUTABLE}"
         ${InstallerLogError} "Restaurado .bak_update -> ${PRODUCT_EXECUTABLE} (rollback)"
         Goto install_binary_ok
         install_binary_no_backup:
         ${InstallerLogError} "Binario ausente e sem backup de rollback — instalacao INVALIDA (exit code nao-zero)"
         StrCpy $UpdateIncomplete "1"
         SetErrorLevel 1
         install_binary_ok:

         ; ── Binário do serviço: valida a cópia (instalação nova E update) ──
         ; O gate é o payload, não o backup: quando o build embute o serviço
         ; (BUILD_HAS_SERVICE=1) o binário TEM de existir em $INSTDIR, senão a
         ; instalação é inválida — antes só se validava em update in-place,
         ; então uma instalação limpa podia ficar sem serviço reportando sucesso.
         ; Em update, se o novo binário não foi copiado, restaura o anterior.
         ${If} "${BUILD_HAS_SERVICE}" == "1"
            IfFileExists "$INSTDIR\${SERVICE_EXECUTABLE}" 0 install_service_binary_missing
            ; O .bak_update é MANTIDO até o health check passar: é dele que sai
            ; o rollback se o serviço não subir com o binário novo.
            ${If} $ServiceBackupCreated == "1"
               ${InstallerLog} "Binario do servico verificado — backup .bak_update mantido para o health check"
            ${Else}
               ${InstallerLog} "Binario do servico verificado: $INSTDIR\${SERVICE_EXECUTABLE} existe"
            ${EndIf}
            Goto install_service_binary_ok
            install_service_binary_missing:
            ${InstallerLogError} "Binario do servico NAO encontrado apos copia: $INSTDIR\${SERVICE_EXECUTABLE} (payload embute o servico — verifique quarentena de antivírus, $INSTDIR efetivo e espaço em disco)"
            IfFileExists "$INSTDIR\${SERVICE_EXECUTABLE}.bak_update" 0 install_service_binary_no_backup
            Rename "$INSTDIR\${SERVICE_EXECUTABLE}.bak_update" "$INSTDIR\${SERVICE_EXECUTABLE}"
            ${InstallerLogError} "Restaurado .bak_update do servico -> ${SERVICE_EXECUTABLE} (rollback de copia)"
            StrCpy $ServiceRollbackDone "1"
            SetErrorLevel 1
            Goto install_service_binary_ok
            install_service_binary_no_backup:
            ${InstallerLogError} "Binario do servico ausente e sem backup — instalacao INCOMPLETA (exit code nao-zero)"
            StrCpy $UpdateIncomplete "1"
            SetErrorLevel 1
            install_service_binary_ok:
         ${EndIf}

         ${If} "${LEGACY_PRODUCT_EXECUTABLE}" != "${PRODUCT_EXECUTABLE}"
            Delete "$INSTDIR\${LEGACY_PRODUCT_EXECUTABLE}"
         ${EndIf}

         CreateShortcut "$SMPROGRAMS\${INFO_PRODUCTNAME}.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}" "" "$INSTDIR\${PRODUCT_EXECUTABLE}" 0
         CreateShortCut "$DESKTOP\${INFO_PRODUCTNAME}.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}" "" "$INSTDIR\${PRODUCT_EXECUTABLE}" 0

         # Garantir estrutura compartilhada em ProgramData
         Call EnsureSharedDataDir

         # Derivar campos canonicos (apiServer, apiInsecure) a partir de serverUrl
         Call DeriveApiFields

         # Salvar configurações do agente
         Call SaveAgentConfig

         # Registrar regra de firewall para runtime local/P2P.
         Call RegisterWindowsFirewallRule

         # Modo de execucao: serviço Windows (core SYSTEM) + Task Scheduler
         # (UI companion no logon de qualquer usuario).
         # PLANO_AGENT_SERVICE_SYSTEM.md, Fase 3.
         Call RegisterAgentService
         # Health check pós-cópia (item 2): só descarta o .bak_update do serviço
         # se ele ficar RUNNING; caso contrário restaura o binário anterior.
         Call VerifyAgentServiceHealth

         # Registrar autostart da UI via Task Scheduler (At log on of any user)
         Call RegisterUIStartupTask

         !insertmacro wails.associateFiles
         !insertmacro wails.associateCustomProtocols

         !insertmacro wails.writeUninstaller
         WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${UNINST_KEY_NAME}" "InstallLocation" "$INSTDIR"

         ; Restart agent apos update silencioso (/UPDATE).
         ; O Task Scheduler so dispara no login; restart imediato garante que o
         ; agente volte ao ar assim que o instalador terminar.
         ${If} $ServiceRollbackDone == "1"
            ${InstallerLogError} "Update mode: rollback do servico aplicado — reinicio automatico ignorado"
            Goto update_restart_done
         ${EndIf}
         ${If} $UpdateMode == "1"
            ${InstallerLog} "Update mode: verificando binario antes de reiniciar..."
            IfFileExists "$INSTDIR\${PRODUCT_EXECUTABLE}" 0 update_binary_missing
            ${InstallerLog} "Update mode: binario encontrado — reiniciando agente..."
            ; IMPORTANTE: reiniciar com --startup-minimized para o agente voltar
            ; direto ao tray (mesmo comportamento do autostart no logon), e nao
            ; abrir janela cheia na sessao do usuario apos o update.
            ;
            ; Sobe o serviço primeiro (core SYSTEM). Se o binário do serviço
            ; existe, NÃO faz Exec da UI: o instalador roda como SYSTEM (sessão
            ; 0) no self-update e criaria a janela no lugar errado. O watchdog
            ; relança a UI em cada sessão ativa.
            nsExec::ExecToLog /OEM '"$SYSDIR\sc.exe" start ${DISCOVERY_SERVICE_NAME}'
            Pop $R4
            IfFileExists "$INSTDIR\${SERVICE_EXECUTABLE}" 0 update_restart_ui
            ${InstallerLog} "Update mode: serviço iniciado — UI será relançada por sessão (watchdog/task de logon)"
            Goto update_restart_done
            update_restart_ui:
            Exec '"$INSTDIR\${PRODUCT_EXECUTABLE}" --startup-minimized --startup-source=update-restart'
            Goto update_restart_done
            update_binary_missing:
            ${InstallerLogError} "Update mode: binario NAO encontrado em $INSTDIR\${PRODUCT_EXECUTABLE} — agente nao reiniciado"
            update_restart_done:
         ${EndIf}
         ; ── Validação final dos binários (antes de declarar sucesso) ──
         ; Fecha a janela entre a checagem pós-cópia e o fim da instalação:
         ; antivírus/quarentena, "Controlled folder access" ou limpeza externa
         ; podem remover os .exe de $INSTDIR depois da cópia. Sem isso o log
         ; dizia "Instalacao concluida com sucesso" com a pasta vazia.
         IfFileExists "$INSTDIR\${PRODUCT_EXECUTABLE}" 0 final_binaries_incomplete
         !if "${BUILD_HAS_SERVICE}" == "1"
            IfFileExists "$INSTDIR\${SERVICE_EXECUTABLE}" 0 final_binaries_incomplete
         !endif
         ${InstallerLog} "Validacao final: binarios presentes em $INSTDIR"
         Goto final_binaries_ok
         final_binaries_incomplete:
         ${InstallerLogError} "Validacao final: binario(s) ausente(s) em $INSTDIR apos a instalacao (possivel quarentena de antivirus ou limpeza externa) — exit code nao-zero"
         StrCpy $UpdateIncomplete "1"
         SetErrorLevel 1
         final_binaries_ok:

         ${If} $ServiceRollbackDone == "1"
            ${InstallerLogError} "Instalacao concluida com ROLLBACK do servico (exit code nao-zero)"
            SetErrorLevel 1
         ${ElseIf} $UpdateIncomplete == "1"
            ; NÃO logar sucesso aqui: o updater correlaciona o installer.log e
            ; trataria como instalação bem-sucedida mesmo com binário antigo.
            ${InstallerLogError} "Instalacao concluida com PENDENCIAS: binario em uso nao foi substituido (exit code nao-zero)"
            SetErrorLevel 1
         ${Else}
            ${InstallerLog} "Instalacao concluida com sucesso"
         ${EndIf}
      !endif
SectionEnd

Section "uninstall"
    !insertmacro wails.setShellContext

   # Encerrar processo antes de limpar binarios
   Call un.UnregisterWindowsFirewallRule

   # Parar serviço ANTES do taskkill (ordem obrigatória, Fase 3/revisão —
   # sem sc stop o SCM reinicia o serviço em ~5s no meio do uninstall).
   Call un.StopAndWaitAgentService

   # Garantir encerramento de qualquer instancia em modo UI/headless.
   nsExec::ExecToLog /OEM '"$SYSDIR\taskkill.exe" /IM "${PRODUCT_EXECUTABLE}" /F /T'
   Pop $R0
   ${If} $R0 != 0
      DetailPrint "Aviso: taskkill retornou codigo $R0 (pode nao haver processo em execucao)."
   ${EndIf}

   ; Binário dedicado do serviço (Fase B/D1) — após un.StopAndWaitAgentService.
   nsExec::ExecToLog /OEM '"$SYSDIR\taskkill.exe" /IM "${SERVICE_EXECUTABLE}" /F /T'
   Pop $R6
   ${If} $R6 != 0
      DetailPrint "Aviso: taskkill do serviço retornou codigo $R6 (pode nao haver processo)."
   ${EndIf}

   ${If} "${LEGACY_PRODUCT_EXECUTABLE}" != "${PRODUCT_EXECUTABLE}"
      nsExec::ExecToLog /OEM '"$SYSDIR\taskkill.exe" /IM "${LEGACY_PRODUCT_EXECUTABLE}" /F /T'
      Pop $R1
      ${If} $R1 != 0
         DetailPrint "Aviso: taskkill legado retornou codigo $R1 (pode nao haver processo legado em execucao)."
      ${EndIf}
   ${EndIf}

   # Executar decommission (DELETE remoto + limpeza local) antes de remover os binarios.
   IfFileExists "$INSTDIR\${PRODUCT_EXECUTABLE}" 0 decommission_done
      DetailPrint "Executando decommission do agente..."
      nsExec::ExecToLog '"$INSTDIR\${PRODUCT_EXECUTABLE}" --agent-delete-cleanup'
      Pop $R3
      ${If} $R3 != 0
         DetailPrint "Aviso: decommission retornou codigo $R3."
      ${EndIf}
decommission_done:

   # Remover serviço Windows (PLANO_AGENT_SERVICE_SYSTEM.md, Fase 3).
   Call un.UnregisterAgentService

   # Remover task agendada de autostart da UI
   Call un.UnregisterUIStartupTask

    RMDir /r "$AppData\${PRODUCT_EXECUTABLE}" # Remove the WebView2 DataPath (legacy)
   ${If} "${LEGACY_PRODUCT_EXECUTABLE}" != "${PRODUCT_EXECUTABLE}"
      RMDir /r "$AppData\${LEGACY_PRODUCT_EXECUTABLE}"
   ${EndIf}

   ReadEnvStr $R2 "ProgramData"
   ${If} $R2 != ""
      RMDir /r "$R2\Discovery\WebView2" # Remove the WebView2 User Data Folder (fixo)
      RMDir /r "$R2\Discovery"
   ${EndIf}

   # Fallback adicional para limpeza do temp local do agente.
   RMDir /r "$WINDIR\Temp\Discovery"

    RMDir /r $INSTDIR

    Delete "$SMPROGRAMS\${INFO_PRODUCTNAME}.lnk"
    Delete "$DESKTOP\${INFO_PRODUCTNAME}.lnk"
   Delete "$SMSTARTUP\${INFO_PRODUCTNAME}.lnk"

    !insertmacro wails.unassociateFiles
    !insertmacro wails.unassociateCustomProtocols

   DeleteRegKey HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${UNINST_KEY_NAME}"

    !insertmacro wails.deleteUninstaller
SectionEnd

# Função para salvar as configurações do agente
# Deriva campos canonicos (apiServer, apiInsecure) a partir de serverUrl.
# Ex: "https://tngplacas.com.br/api/" -> apiServer="tngplacas.com.br", apiInsecure="0"
Function DeriveApiFields
   StrCpy $ApiServer ""
   StrCpy $ApiInsecure "0"

   ${If} $ServerUrl == ""
      Return
   ${EndIf}

   # Detecta HTTP (nao seguro).
   ${WordFind} "$ServerUrl" "http://" "*" $R0
   ${If} $R0 != "$ServerUrl"
      StrCpy $ApiInsecure "1"
      StrCpy $R1 "$ServerUrl" "" 7  ; remove "http://"
   ${Else}
      ${WordFind} "$ServerUrl" "https://" "*" $R0
      ${If} $R0 != "$ServerUrl"
         StrCpy $R1 "$ServerUrl" "" 8  ; remove "https://"
      ${Else}
         StrCpy $R1 "$ServerUrl"  ; sem scheme
      ${EndIf}
   ${EndIf}

   ; Extrai hostname: pega da posicao 0 ate a primeira "/".
   ${WordFind} "$R1" "/" "+1" $ApiServer
   ${If} $ApiServer == ""
      StrCpy $ApiServer "$R1"
   ${EndIf}
FunctionEnd

Function SaveAgentConfig
   # Build de update não altera configuração local existente.
   ${If} "${BUILD_UPDATE_INSTALL}" == "1"
      DetailPrint "Update build: mantendo configuração existente sem sobrescrita"
      Return
   ${EndIf}

   # Runtime update (/UPDATE) também não altera configuração local existente.
   ${If} $UpdateMode == "1"
      DetailPrint "Update mode (/UPDATE): mantendo configuração existente sem sobrescrita"
      Return
   ${EndIf}

   # Config compartilhada em ProgramData para suportar múltiplos usuários.
   ReadEnvStr $R0 "ProgramData"
   ${If} $R0 == ""
      MessageBox MB_ICONSTOP "Pasta ProgramData nao encontrada. Nao foi possivel gravar a configuracao do agente."
      Abort
   ${EndIf}

   DetailPrint "Salvando config em $R0\Discovery\config.json"
   ClearErrors
   CreateDirectory "$R0\Discovery"
   ${If} ${Errors}
      DetailPrint "ERRO: nao foi possivel criar a pasta $R0\Discovery"
      MessageBox MB_ICONSTOP "Falha ao criar a pasta de configuracao em $R0\Discovery"
      Abort
   ${EndIf}

   ; Garantir permissao multiusuario para runtime sem servico Windows.
   ; Usa SIDs bem conhecidos para evitar falha em SOs localizados.
   ; /inheritance:e + /T aplica em filhos existentes (ex.: config.json legado).
   nsExec::ExecToLog /OEM '"$SYSDIR\icacls.exe" "$R0\Discovery" /inheritance:e /grant "*S-1-5-18:(OI)(CI)(F)" "*S-1-5-32-544:(OI)(CI)(F)" "*S-1-5-32-545:(OI)(CI)(M)" /T /C /Q'
   Pop $R9
   ${If} $R9 != 0
      DetailPrint "Aviso: nao foi possivel ajustar ACL de $R0\Discovery (icacls codigo $R9)"
   ${EndIf}

   StrCpy $R1 "$R0\Discovery\config.json"
   StrCpy $R2 "$INSTDIR\config.json"
   DetailPrint "Mesclando configuracao em $R1"

   ; Grava JSON valido com FileWrite (evita corrupcao do plugin nsJSON com strings).
   ; Fallback para $INSTDIR se ProgramData falhar (ex.: maquina sem disco C: padrao).

   ClearErrors
   FileOpen $0 "$R1" w
   ${If} ${Errors}
      ClearErrors
      Goto config_fallback
   ${EndIf}

   FileWrite $0 "{$\r$\n"
   ${If} $GenericMode != "1"
      ${If} $ApiServer != ""
         FileWrite $0 '  "serverapi": "$ApiServer",$\r$\n'
      ${EndIf}
      ${If} $ApiInsecure == "1"
         FileWrite $0 '  "apiInsecure": true,$\r$\n'
      ${EndIf}
      ${If} $ServerUrl != ""
         FileWrite $0 '  "serverUrl": "$ServerUrl",$\r$\n'
      ${EndIf}
      ${If} $ServerKey != ""
         FileWrite $0 '  "deployToken": "$ServerKey",$\r$\n'
      ${EndIf}
   ${EndIf}
   ${If} $AutoProvisioning == "1"
      FileWrite $0 '  "autoProvisioning": true,$\r$\n'
   ${Else}
      FileWrite $0 '  "autoProvisioning": false,$\r$\n'
   ${EndIf}
   ${If} $AllowInsecureTls == "1"
      FileWrite $0 '  "allowInsecureTls": true,$\r$\n'
   ${EndIf}
   ${If} $AutoProvisioning == "1"
      FileWrite $0 '  "p2p": {$\r$\n'
      FileWrite $0 '    "enabled": true$\r$\n'
      FileWrite $0 '  }$\r$\n'
   ${Else}
      FileWrite $0 '  "p2p": {$\r$\n'
      FileWrite $0 '    "enabled": false$\r$\n'
      FileWrite $0 '  }$\r$\n'
   ${EndIf}
   FileWrite $0 "}$\r$\n"
   FileClose $0
   Goto config_done

config_fallback:
   DetailPrint "Falha ao gravar em $R1, tentando fallback $R2"
   ClearErrors
   FileOpen $0 "$R2" w
   ${If} ${Errors}
      MessageBox MB_ICONSTOP "Falha ao gravar a configuracao do agente. Verifique permissoes em $R0\Discovery e $INSTDIR."
      Abort
   ${EndIf}

   FileWrite $0 "{$\r$\n"
   ${If} $GenericMode != "1"
      ${If} $ApiServer != ""
         FileWrite $0 '  "serverapi": "$ApiServer",$\r$\n'
      ${EndIf}
      ${If} $ApiInsecure == "1"
         FileWrite $0 '  "apiInsecure": true,$\r$\n'
      ${EndIf}
      ${If} $ServerUrl != ""
         FileWrite $0 '  "serverUrl": "$ServerUrl",$\r$\n'
      ${EndIf}
      ${If} $ServerKey != ""
         FileWrite $0 '  "deployToken": "$ServerKey",$\r$\n'
      ${EndIf}
   ${EndIf}
   ${If} $AutoProvisioning == "1"
      FileWrite $0 '  "autoProvisioning": true,$\r$\n'
   ${Else}
      FileWrite $0 '  "autoProvisioning": false,$\r$\n'
   ${EndIf}
   ${If} $AllowInsecureTls == "1"
      FileWrite $0 '  "allowInsecureTls": true,$\r$\n'
   ${EndIf}
   ${If} $AutoProvisioning == "1"
      FileWrite $0 '  "p2p": {$\r$\n'
      FileWrite $0 '    "enabled": true$\r$\n'
      FileWrite $0 '  }$\r$\n'
   ${Else}
      FileWrite $0 '  "p2p": {$\r$\n'
      FileWrite $0 '    "enabled": false$\r$\n'
      FileWrite $0 '  }$\r$\n'
   ${EndIf}
   FileWrite $0 "}$\r$\n"
   FileClose $0

config_done:

   # Limpa installer.json legado
   Delete "$R0\Discovery\installer.json"
   Delete "$INSTDIR\installer.json"

   DetailPrint "Config salvo com sucesso em $R0\Discovery\config.json"
FunctionEnd

Function LaunchInstalledApp
   # Em bootstrap, a segunda etapa instala em modo silencioso e o Finish page
   # deste instalador e o unico ponto interativo para abrir a UI imediatamente.
   #
   # IMPORTANTE (UI sem admin): o instalador roda elevado. Executar o
   # discovery-agent.exe diretamente herdaria o token High IL e a UI abriria
   # elevada — exatamente o que o manifest asInvoker remove. Delegar ao
   # explorer.exe faz o shell interativo (Medium IL) abrir a UI no nível do
   # usuário. Só há Finish page em instalação interativa, então o shell existe.
   StrCpy $R0 "$INSTDIR\${PRODUCT_EXECUTABLE}"
   ${If} ${FileExists} "$R0"
      DetailPrint "Abrindo agente (como o usuário interativo, sem elevação) em $R0"
      Exec 'explorer.exe "$R0"'
      Return
   ${EndIf}

   # Fallback: usar caminho registrado pelo instalador completo.
   ReadRegStr $R1 HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${UNINST_KEY_NAME}" "InstallLocation"
   ${If} $R1 != ""
      StrCpy $R0 "$R1\${PRODUCT_EXECUTABLE}"
      ${If} ${FileExists} "$R0"
         DetailPrint "Abrindo agente (como o usuário interativo, sem elevação) em $R0"
         Exec 'explorer.exe "$R0"'
         Return
      ${EndIf}
   ${EndIf}

   DetailPrint "Aviso: nao foi possivel localizar ${PRODUCT_EXECUTABLE} para abrir automaticamente."
FunctionEnd

Function DownloadAndRunStage2
   ${If} $PayloadUrl == ""
      MessageBox MB_ICONSTOP "Payload URL nao configurada no bootstrapper."
      Abort
   ${EndIf}

   DetailPrint "Payload URL: $PayloadUrl"

   ${If} $PayloadFileName == ""
      StrCpy $PayloadFileName "discovery-stage2-installer.exe"
   ${EndIf}

   ; Staging na pasta dedicada do agent (%WINDIR%\Temp\Discovery) — a mesma
   ; usada por platform.TempDir no runtime — nunca na raiz de %TEMP%.
   StrCpy $R6 "$WINDIR\Temp\Discovery\Bootstrap"
   CreateDirectory "$R6"
   StrCpy $R7 "$R6\$PayloadFileName"

   DetailPrint "Baixando instalador completo de segunda etapa..."

   # Evita dependencia de BITS/bitsadmin (pode falhar com acesso negado em builds recentes).
   # Prioriza curl nativo do Windows e usa certutil como fallback.
   nsExec::ExecToLog /OEM '"$SYSDIR\curl.exe" -f -L --retry 2 --connect-timeout 20 --output "$R7" "$PayloadUrl"'
   Pop $R0

   ${If} $R0 != 0
      DetailPrint "Aviso: curl falhou (codigo: $R0). Tentando fallback com certutil..."
      nsExec::ExecToLog /OEM '"$SYSDIR\certutil.exe" -urlcache -split -f "$PayloadUrl" "$R7"'
      Pop $R0
   ${EndIf}

   ${If} $R0 != 0
      MessageBox MB_ICONSTOP "Falha ao baixar instalador completo (codigo: $R0). Verifique a conectividade com o servidor."
      Abort
   ${EndIf}

   ${IfNot} ${FileExists} "$R7"
      MessageBox MB_ICONSTOP "Falha ao baixar instalador completo: arquivo nao encontrado apos download."
      Abort
   ${EndIf}

   # ── Dynamic SHA256 validation ──────────────────────────────────────────────
   # Fetches the expected SHA256 from the API at install time (not compile time).
   # The endpoint is derived from the payload URL: <payload>/sha256
   # This avoids bootstrap breakage when the stage2 is rebuilt after generation.
   #
   # Fallback chain:
   #  1. Dynamic: fetch expected hash from $PayloadUrl/sha256 (curl/certutil)
   #  2. Static:  use compile-time embedded hash (backward compat, rare)
   #  3. None:    skip validation with warning
   # ────────────────────────────────────────────────────────────────────────────
   StrCpy $R8 "$R6\expected.sha256"
   nsExec::ExecToLog /OEM '"$SYSDIR\curl.exe" -f -L --retry 2 --connect-timeout 20 --output "$R8" "$PayloadUrl/sha256"'
   Pop $R0

   ${If} $R0 != 0
      nsExec::ExecToLog /OEM '"$SYSDIR\certutil.exe" -urlcache -split -f "$PayloadUrl/sha256" "$R8"'
      Pop $R0
   ${EndIf}

   ${If} $R0 != 0
      # Dynamic fetch failed — fall back to compile-time hash or skip
      ${If} $PayloadSha256 != ""
         DetailPrint "Validando integridade SHA256 do payload (hash estatico)..."
         nsExec::ExecToLog /OEM '"$SYSDIR\cmd.exe" /c certutil -hashfile "$R7" SHA256 | findstr /i /c:"$PayloadSha256"'
         Pop $R0
         ${If} $R0 != 0
            MessageBox MB_ICONSTOP "Falha na validacao de integridade do payload (hash mismatch)."
            Abort
         ${EndIf}
      ${Else}
         # A12: sem hash válido NÃO executa o stage2 (baixado da mesma origem
         # do payload — verificação auto-referencial não é prova de integridade).
         MessageBox MB_ICONSTOP "Integridade do payload nao pode ser validada (endpoint SHA256 indisponivel e hash estatico ausente). Instalacao abortada por seguranca."
         Abort
      ${EndIf}
   ${Else}
      DetailPrint "Validando integridade SHA256 do payload..."
      # findstr /g: reads expected hash from downloaded file; certutil output
      # contains computed hash on its own line — findstr matches it case-insensitively.
      nsExec::ExecToLog /OEM '"$SYSDIR\cmd.exe" /c certutil -hashfile "$R7" SHA256 | findstr /i /g:"$R8"'
      Pop $R0
      ${If} $R0 != 0
         MessageBox MB_ICONSTOP "Falha na validacao de integridade do payload (hash mismatch)."
         Abort
      ${EndIf}
   ${EndIf}

   DetailPrint "Executando instalador completo de segunda etapa..."
   ${If} $GenericMode == "1"
      ${If} $UpdateMode == "1"
         nsExec::ExecToLog '"$R7" /S /UPDATE /GENERIC /BOOTSTRAP_STAGE2 /AUTO_PROVISIONING=$AutoProvisioning /ALLOW_INSECURE_TLS=$AllowInsecureTls'
         Pop $R0
      ${Else}
         nsExec::ExecToLog '"$R7" /S /GENERIC /BOOTSTRAP_STAGE2 /AUTO_PROVISIONING=$AutoProvisioning /ALLOW_INSECURE_TLS=$AllowInsecureTls'
         Pop $R0
      ${EndIf}
   ${Else}
      ${If} $UpdateMode == "1"
         nsExec::ExecToLog '"$R7" /S /UPDATE /BOOTSTRAP_STAGE2 /URL="$ServerUrl" /KEY="$ServerKey" /AUTO_PROVISIONING=$AutoProvisioning /ALLOW_INSECURE_TLS=$AllowInsecureTls'
         Pop $R0
      ${Else}
         nsExec::ExecToLog '"$R7" /S /BOOTSTRAP_STAGE2 /URL="$ServerUrl" /KEY="$ServerKey" /AUTO_PROVISIONING=$AutoProvisioning /ALLOW_INSECURE_TLS=$AllowInsecureTls'
         Pop $R0
      ${EndIf}
   ${EndIf}

   ${If} $R0 != 0
      MessageBox MB_ICONSTOP "A instalacao de segunda etapa falhou (codigo: $R0)."
      Abort
   ${EndIf}

   DetailPrint "Segunda etapa concluida com sucesso."
FunctionEnd

Function EnsureSharedDataDir
   ReadEnvStr $R0 "ProgramData"
   ${If} $R0 == ""
      Return
   ${EndIf}
   CreateDirectory "$R0\Discovery"
   CreateDirectory "$R0\Discovery\logs"

   ; Aplicar ACL no diretorio compartilhado para execucao por usuarios padrao.
   ; SIDs evitam dependencia de nomes localizados de grupos.
   ; /inheritance:e + /T cobre arquivos preexistentes no diretorio.
   nsExec::ExecToLog /OEM '"$SYSDIR\icacls.exe" "$R0\Discovery" /inheritance:e /grant "*S-1-5-18:(OI)(CI)(F)" "*S-1-5-32-544:(OI)(CI)(F)" "*S-1-5-32-545:(OI)(CI)(M)" /T /C /Q'
   Pop $R9
   ${If} $R9 != 0
      DetailPrint "Aviso: nao foi possivel ajustar ACL de $R0\Discovery (icacls codigo $R9)"
   ${EndIf}
FunctionEnd

Function PrepareForInPlaceUpdate
   ${InstallerLog} "PrepareForInPlaceUpdate: iniciando"
   DetailPrint "Preparando atualizacao in-place (encerrando instancias em execucao)..."

   ; ── Serviço Windows: parar ANTES do taskkill (ordem obrigatória,
   ; PLANO_AGENT_SERVICE_SYSTEM.md Fase 3/revisão). Sem `sc stop` antes,
   ; o SCM interpreta o taskkill como crash e as failure actions reiniciam
   ; o serviço em ~5s, no meio da cópia dos binários (race).
   ${InstallerLog} "Parando servico ${DISCOVERY_SERVICE_NAME} (antes do taskkill)..."
   Call StopAndWaitAgentService

   ; Tentar remover startup task antiga antes de atualizar binarios.
   ${InstallerLog} "Removendo startup task antiga..."
   Call UnregisterUIStartupTask

   # Garantir que nenhuma instancia do app permaneceu em execucao.
   # IMPORTANTE: NAO usar /T (tree kill) para o agente. O instalador NSIS roda
   # como processo filho do agente (lancado via ShellExecuteEx/CreateProcess).
   # `taskkill /T` mataria o agente E o proprio instalador (filho), abortando
   # o update no meio da copia. Sem /T, apenas o agente e encerrado e o
   # instalador, que ja foi destacado como processo independente, sobrevive
   # para concluir a instalacao e reiniciar o agente.
   nsExec::ExecToLog /OEM '"$SYSDIR\taskkill.exe" /IM "${PRODUCT_EXECUTABLE}" /F'
   Pop $R0
   ${If} $R0 != 0
      ${InstallerLog} "taskkill ${PRODUCT_EXECUTABLE}: codigo $R0 (pode nao haver processo)"
   ${Else}
      ${InstallerLog} "taskkill ${PRODUCT_EXECUTABLE}: processo encerrado"
   ${EndIf}

   ; Binário dedicado do serviço (Fase B/D1): após sc stop + wait STOPPED,
   ; mata qualquer residual do discovery-service.exe antes de sobrescrever.
   nsExec::ExecToLog /OEM '"$SYSDIR\taskkill.exe" /IM "${SERVICE_EXECUTABLE}" /F'
   Pop $R5
   ${If} $R5 != 0
      ${InstallerLog} "taskkill ${SERVICE_EXECUTABLE}: codigo $R5 (pode nao haver processo)"
   ${EndIf}

   ${If} "${LEGACY_PRODUCT_EXECUTABLE}" != "${PRODUCT_EXECUTABLE}"
      nsExec::ExecToLog /OEM '"$SYSDIR\taskkill.exe" /IM "${LEGACY_PRODUCT_EXECUTABLE}" /F /T'
      Pop $R1
      ${If} $R1 != 0
         ${InstallerLog} "taskkill legado ${LEGACY_PRODUCT_EXECUTABLE}: codigo $R1"
      ${EndIf}
   ${EndIf}

   # Esperar o processo ser totalmente encerrado e o handle do arquivo liberado.
   # O Windows pode levar 1-3 segundos para liberar o lock do .exe apos taskkill.
   # Sem esse delay, o File do NSIS falha ao sobrescrever o binario (Access Denied).
   ${InstallerLog} "Aguardando 2s para liberacao de handles..."
   Sleep 2000

   # Retry: tentar renomear o executavel atual para liberar o path.
   # O Windows permite renomear um .exe mesmo que ele esteja em execução
   # (após taskkill, o processo está morto mas pode ainda não ter liberado
   # o handle). O rename para .bak_update libera o path original para o
   # NSIS copiar o novo binário sem "Access Denied".
   # NOTA: o .bak_update é deletado APÓS a cópia bem-sucedida (na Section).
   #
   # Backup OBSOLETO: updates abortados podem deixar um .bak_update para trás.
   # Rename para um destino existente falha no Windows — sem remover o antigo,
   # o path original nunca é liberado e o NSIS "instala" mantendo o binário
   # velho (update silencioso falso). Só remove quando o binário ATUAL existe:
   # ele é o alvo de rollback mais novo e substitui o backup anterior.
   IfFileExists "$INSTDIR\${PRODUCT_EXECUTABLE}" 0 lock_check_done
   Delete "$INSTDIR\${PRODUCT_EXECUTABLE}.bak_update"
   StrCpy $R3 0
   retry_lock_check:
      ${If} $R3 >= 5
         ${InstallerLogError} "lock check: falhou apos 5 tentativas — binario da UI pode nao ter sido substituido"
         StrCpy $UpdateIncomplete "1"
         Goto lock_check_done
      ${EndIf}
      IfFileExists "$INSTDIR\${PRODUCT_EXECUTABLE}" 0 lock_check_done
      ClearErrors
      Rename "$INSTDIR\${PRODUCT_EXECUTABLE}" "$INSTDIR\${PRODUCT_EXECUTABLE}.bak_update"
      ${If} ${Errors}
         IntOp $R3 $R3 + 1
         ${InstallerLog} "lock check: tentativa $R3/5 falhou — aguardando liberacao do arquivo..."
         Sleep 1500
         Goto retry_lock_check
      ${Else}
         ${InstallerLog} "lock check: executavel renomeado para .bak_update — path liberado para nova copia"
      ${EndIf}
   lock_check_done:

   # ── Backup do binário do SERVIÇO (rollback do self-update) ──
   # Renomeia discovery-service.exe -> .bak_update antes da cópia. O backup é
   # preservado até o health check (VerifyAgentServiceHealth); se o serviço não
   # subir com o binário novo, RollbackAgentServiceUpdate restaura este arquivo.
   # Se não existir (build sem serviço), nada a fazer.
   IfFileExists "$INSTDIR\${SERVICE_EXECUTABLE}" 0 service_backup_done
   # Mesmo motivo do binário da UI: um .bak_update obsoleto (update abortado)
   # impede o Rename e o serviço continuaria rodando o binário antigo. O
   # binário atual (que vai virar o novo backup) é sempre o alvo melhor.
   Delete "$INSTDIR\${SERVICE_EXECUTABLE}.bak_update"
   StrCpy $R3 0
   service_backup_retry:
      ${If} $R3 >= 5
         ${InstallerLogError} "backup do servico: falhou apos 5 tentativas — binario do servico pode nao ter sido substituido"
         StrCpy $UpdateIncomplete "1"
         Goto service_backup_done
      ${EndIf}
      ClearErrors
      Rename "$INSTDIR\${SERVICE_EXECUTABLE}" "$INSTDIR\${SERVICE_EXECUTABLE}.bak_update"
      ${If} ${Errors}
         IntOp $R3 $R3 + 1
         ${InstallerLog} "backup do servico: tentativa $R3/5 falhou — aguardando liberacao..."
         Sleep 1500
         Goto service_backup_retry
      ${Else}
         StrCpy $ServiceBackupCreated "1"
         ${InstallerLog} "backup do servico criado: ${SERVICE_EXECUTABLE}.bak_update"
      ${EndIf}
   service_backup_done:
FunctionEnd

Function RegisterWindowsFirewallRule
   DetailPrint "Registrando regras de Windows Firewall (UI + servico + portas P2P)"

   # netsh advfirewall: nativo do Windows, sem dependencia de PowerShell ou plugins.
   # Remove regras anteriores (idempotente) e cria novas.

   # 1) UI (discovery-agent.exe) — mantida por compatibilidade/remote session.
   nsExec::ExecToLog /OEM '"$SYSDIR\netsh.exe" advfirewall firewall delete rule name="${DISCOVERY_FIREWALL_RULE_NAME}"'
   Pop $R0
   nsExec::ExecToLog /OEM '"$SYSDIR\netsh.exe" advfirewall firewall add rule name="${DISCOVERY_FIREWALL_RULE_NAME}" dir=in action=allow program="$INSTDIR\${PRODUCT_EXECUTABLE}" enable=yes profile=any'
   Pop $R0
   ${If} $R0 != 0
      DetailPrint "Aviso: falha ao registrar regra de Windows Firewall para ${PRODUCT_EXECUTABLE}. Codigo: $R0"
   ${EndIf}

   # 2) SERVICO (discovery-service.exe) — onde o P2P realmente escuta.
   IfFileExists "$INSTDIR\${SERVICE_EXECUTABLE}" 0 fw_service_missing
   nsExec::ExecToLog /OEM '"$SYSDIR\netsh.exe" advfirewall firewall delete rule name="${DISCOVERY_SERVICE_FIREWALL_RULE_NAME}"'
   Pop $R1
   nsExec::ExecToLog /OEM '"$SYSDIR\netsh.exe" advfirewall firewall add rule name="${DISCOVERY_SERVICE_FIREWALL_RULE_NAME}" dir=in action=allow program="$INSTDIR\${SERVICE_EXECUTABLE}" enable=yes profile=any'
   Pop $R1
   ${If} $R1 != 0
      DetailPrint "Aviso: falha ao registrar regra de Firewall para ${SERVICE_EXECUTABLE}. Codigo: $R1"
   ${EndIf}
   Goto fw_service_done
   fw_service_missing:
   DetailPrint "Aviso: ${SERVICE_EXECUTABLE} ausente — regra de firewall do servico nao registrada"
   fw_service_done:

   # 3) Portas P2P: faixa HTTP de transferencia + porta fixa do libp2p.
   #    TCP cobre o transporte padrao (tcpOnly=true); UDP cobre QUIC/v1 quando
   #    tcpOnly=false (sem esta regra o QUIC ficaria bloqueado e o handshake
   #    cairia em fallback mais lento).
   nsExec::ExecToLog /OEM '"$SYSDIR\netsh.exe" advfirewall firewall delete rule name="${DISCOVERY_P2P_PORTS_RULE_NAME}"'
   Pop $R2
   nsExec::ExecToLog /OEM '"$SYSDIR\netsh.exe" advfirewall firewall add rule name="${DISCOVERY_P2P_PORTS_RULE_NAME}" dir=in action=allow protocol=TCP localport=${DISCOVERY_P2P_HTTP_PORT_RANGE},${DISCOVERY_P2P_LIBP2P_PORT} enable=yes profile=any'
   Pop $R2
   ${If} $R2 != 0
      DetailPrint "Aviso: falha ao registrar regra de portas P2P (TCP). Codigo: $R2"
   ${EndIf}
   nsExec::ExecToLog /OEM '"$SYSDIR\netsh.exe" advfirewall firewall delete rule name="${DISCOVERY_P2P_PORTS_UDP_RULE_NAME}"'
   Pop $R3
   nsExec::ExecToLog /OEM '"$SYSDIR\netsh.exe" advfirewall firewall add rule name="${DISCOVERY_P2P_PORTS_UDP_RULE_NAME}" dir=in action=allow protocol=UDP localport=${DISCOVERY_P2P_LIBP2P_PORT} enable=yes profile=any'
   Pop $R3
   ${If} $R3 != 0
      DetailPrint "Aviso: falha ao registrar regra de portas P2P (UDP). Codigo: $R3"
   ${EndIf}
FunctionEnd

Function RegisterUIStartupTask
   DetailPrint "Registrando tarefa de autostart da UI (${DISCOVERY_UI_TASK_NAME})"

   ; Limpar atalho legado de startup (migracao)
   Delete "$SMSTARTUP\${INFO_PRODUCTNAME}.lnk"

   ; Evita fragilidade de quoting no -Command inline: gera script temporario
   ; na pasta dedicada do agent (%WINDIR%\Temp\Discovery), nunca na raiz de %TEMP%.
   CreateDirectory "$WINDIR\Temp\Discovery"
   StrCpy $R9 "$WINDIR\Temp\Discovery\discovery_ui_task_reg.ps1"
   ClearErrors
   FileOpen $R8 "$R9" w
   ${If} ${Errors}
      Goto ui_startup_fallback
   ${EndIf}

   FileWrite $R8 "try {$\r$\n"
   FileWrite $R8 "  $$action = New-ScheduledTaskAction -Execute '$INSTDIR\${PRODUCT_EXECUTABLE}' -Argument '--startup-minimized --startup-source=task-scheduler'$\r$\n"
   FileWrite $R8 "  $$trigger = New-ScheduledTaskTrigger -AtLogOn$\r$\n"
   FileWrite $R8 "  $$trigger.Delay = 'PT30S'$\r$\n"
   # LeastPrivilege (item 1): a UI companheira NÃO pede elevação. Ela roda com
   # o token interativo do usuário (Medium IL) e tudo que exige privilégio é
   # feito pelo serviço DiscoveryAgent via IPC. Não voltar para Highest: isso
   # reintroduziria o UAC no logon e quebraria o autostart sem prompt.
   FileWrite $R8 "  $$principal = New-ScheduledTaskPrincipal -GroupId 'S-1-5-32-545' -RunLevel LeastPrivilege$\r$\n"
   FileWrite $R8 "  Register-ScheduledTask -TaskName '${DISCOVERY_UI_TASK_NAME}' -Action $$action -Trigger $$trigger -Principal $$principal -Description 'Discovery UI autostart for any logged-on user' -Force | Out-Null$\r$\n"
   FileWrite $R8 "  exit 0$\r$\n"
   FileWrite $R8 "} catch {$\r$\n"
   FileWrite $R8 "  Write-Output $$_.Exception.Message$\r$\n"
   FileWrite $R8 "  exit 1$\r$\n"
   FileWrite $R8 "}$\r$\n"
   FileClose $R8

   nsExec::ExecToLog /OEM '"$SYSDIR\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "$R9"'
   Pop $R0
   Delete "$R9"

   ${If} $R0 == 0
      Return
   ${EndIf}

ui_startup_fallback:
   DetailPrint "Aviso: falha ao registrar Scheduled Task (${DISCOVERY_UI_TASK_NAME}). Aplicando fallback via Startup folder."
   ClearErrors
   ; NOTA: o manifest da UI é asInvoker e a Scheduled Task usa LeastPrivilege —
   ; nenhum dos dois pede UAC. O atalho no Startup folder é o fallback para
   ; máquinas sem Task Scheduler e sobe a UI no mesmo nível do usuário.
   CreateShortCut "$SMSTARTUP\${INFO_PRODUCTNAME}.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}" "--startup-minimized --startup-source=startup-link" "$INSTDIR\${PRODUCT_EXECUTABLE}" 0
   ${If} ${Errors}
      MessageBox MB_ICONSTOP "Falha ao registrar autostart da UI (${DISCOVERY_UI_TASK_NAME}) em Scheduled Task e Startup folder."
      Abort
   ${EndIf}

   DetailPrint "Fallback aplicado: autostart da UI registrado via Startup folder."
FunctionEnd

Function un.UnregisterWindowsFirewallRule
   DetailPrint "Removendo regras de Windows Firewall do agente"

   # netsh advfirewall: nativo do Windows, sem PowerShell.
   nsExec::ExecToLog /OEM '"$SYSDIR\netsh.exe" advfirewall firewall delete rule name="${DISCOVERY_FIREWALL_RULE_NAME}"'
   Pop $R0
   nsExec::ExecToLog /OEM '"$SYSDIR\netsh.exe" advfirewall firewall delete rule name="${DISCOVERY_SERVICE_FIREWALL_RULE_NAME}"'
   Pop $R1
   nsExec::ExecToLog /OEM '"$SYSDIR\netsh.exe" advfirewall firewall delete rule name="${DISCOVERY_P2P_PORTS_RULE_NAME}"'
   Pop $R2
   nsExec::ExecToLog /OEM '"$SYSDIR\netsh.exe" advfirewall firewall delete rule name="${DISCOVERY_P2P_PORTS_UDP_RULE_NAME}"'
   Pop $R3

   ${If} $R0 != 0
      DetailPrint "Aviso: falha ao remover regra da UI. Codigo: $R0"
   ${EndIf}
FunctionEnd

Function UnregisterUIStartupTask
   DetailPrint "Removendo tarefa de autostart da UI (${DISCOVERY_UI_TASK_NAME})"
   # Só deleta se a task existir (schtasks /Delete em task inexistente imprime
   # "ERRO: O sistema não pode encontrar o arquivo especificado").
   # nsExec::Exec (sem ToLog) suprime a saída do Query.
   nsExec::Exec '"$SYSDIR\schtasks.exe" /Query /TN "${DISCOVERY_UI_TASK_NAME}"'
   Pop $R0
   ${If} $R0 == 0
      nsExec::ExecToLog /OEM '"$SYSDIR\schtasks.exe" /Delete /TN "${DISCOVERY_UI_TASK_NAME}" /F'
      Pop $R0
   ${EndIf}
   Delete "$SMSTARTUP\${INFO_PRODUCTNAME}.lnk"
FunctionEnd

Function un.UnregisterUIStartupTask
   DetailPrint "Removendo tarefa de autostart da UI (${DISCOVERY_UI_TASK_NAME})"
   # Só deleta se a task existir (mesma proteção da variante install).
   nsExec::Exec '"$SYSDIR\schtasks.exe" /Query /TN "${DISCOVERY_UI_TASK_NAME}"'
   Pop $R0
   ${If} $R0 == 0
      nsExec::ExecToLog /OEM '"$SYSDIR\schtasks.exe" /Delete /TN "${DISCOVERY_UI_TASK_NAME}" /F'
      Pop $R0
   ${EndIf}
   Delete "$SMSTARTUP\${INFO_PRODUCTNAME}.lnk"
FunctionEnd

# ── Windows Service (PLANO_AGENT_SERVICE_SYSTEM.md, Fase 3) ────────────────

# StopAndWaitAgentService: para o serviço (desarma failure recovery do SCM)
# e aguarda STOPPED antes de retornar (revisão 2026-09-04: `sc stop` retorna
# antes do serviço parar de fato — sem wait, o rename .bak_update falha com
# o .exe carregado e o taskkill sem sc stop dispara o restart em ~5s).
Function StopAndWaitAgentService
   # Fast-path: se o serviço não existe (sc query -> 1060), sai sem esperar.
   # Instalação sem o binário do serviço não deve gastar 30s no loop nem
   # poluir o log com o dump do help do sc ("opção inválida").
   nsExec::ExecToLog /OEM '"$SYSDIR\sc.exe" query ${DISCOVERY_SERVICE_NAME}'
   Pop $R2
   ${If} $R2 == 1060
      DetailPrint "Serviço ${DISCOVERY_SERVICE_NAME} não existe — nada a parar"
      Goto svc_wait_done
   ${EndIf}

   nsExec::ExecToLog /OEM '"$SYSDIR\sc.exe" stop ${DISCOVERY_SERVICE_NAME}'
   Pop $R0

   StrCpy $R1 0
   svc_wait_stopped:
      ${If} $R1 >= 30
         DetailPrint "Aviso: serviço ${DISCOVERY_SERVICE_NAME} não parou em 30s — prosseguindo"
         Goto svc_wait_done
      ${EndIf}
      # O pipe precisa do cmd.exe: nsExec não interpreta pipes de shell e o sc
      # receberia "|" como opção inválida (dump do help no log a cada iteração).
      nsExec::ExecToLog /OEM '"$SYSDIR\cmd.exe" /c sc.exe query ${DISCOVERY_SERVICE_NAME} | "$SYSDIR\find.exe" "STOPPED"'
      Pop $R2
      ${If} $R2 == 0
         DetailPrint "Serviço ${DISCOVERY_SERVICE_NAME} STOPPED"
         Goto svc_wait_done
      ${EndIf}
      Sleep 1000
      IntOp $R1 $R1 + 1
      Goto svc_wait_stopped
   svc_wait_done:
FunctionEnd

# RegisterAgentService: cria/inicia o serviço do core do agent.
Function RegisterAgentService
   DetailPrint "Registrando serviço ${DISCOVERY_SERVICE_NAME}..."

   # Guarda: só registra se o binário dedicado do serviço foi copiado para
   # $INSTDIR. Builds do pipeline PS (sem /DARG_SERVICE_AMD64_BINARY) não
   # embutem discovery-service.exe — registrar um serviço apontando para
   # binário inexistente faz o `sc start` falhar com erro 2 (file not found)
   # e deixa um serviço quebrado no SCM. Sem o serviço, a UI sobe standalone
   # via Task Scheduler (fallback já existente).
   IfFileExists "$INSTDIR\${SERVICE_EXECUTABLE}" 0 register_agent_svc_skip

   # Idempotente: remove definição anterior se existir.
   nsExec::ExecToLog /OEM '"$SYSDIR\sc.exe" stop ${DISCOVERY_SERVICE_NAME}'
   Pop $R0
   nsExec::ExecToLog /OEM '"$SYSDIR\sc.exe" delete ${DISCOVERY_SERVICE_NAME}'
   Pop $R0
   Sleep 1500

   # start= auto: inicia o mais cedo possível no boot (SCM inicia todos os
   # serviços auto em paralelo). Antes era delayed-auto (~2min de atraso),
   # o que fazia a UI (Task AtLogOn) abrir antes do serviço — problema
   # relatado em produção: agente em modo interface primeiro, serviço depois.
   # O startup interno do agente é staged/desacoplado, então não há risco de
   # competir com serviços críticos do Windows.
   nsExec::ExecToLog /OEM '"$SYSDIR\sc.exe" create ${DISCOVERY_SERVICE_NAME} binPath= "\"$INSTDIR\${SERVICE_EXECUTABLE}\"" start= auto obj= LocalSystem DisplayName= "Discovery Agent Service"'
   Pop $R0
   ${If} $R0 != 0
      # Serviço legado apontando para o binário UI (--service): atualiza o
      # binPath para o binário dedicado do serviço (Fase B/D1 do plano).
      nsExec::ExecToLog /OEM '"$SYSDIR\sc.exe" config ${DISCOVERY_SERVICE_NAME} binPath= "\"$INSTDIR\${SERVICE_EXECUTABLE}\"" start= auto'
      Pop $R0
      ${If} $R0 != 0
         DetailPrint "Aviso: sc create/config falhou (codigo $R0) — agente seguirá em modo standalone via Task"
      ${EndIf}
   ${EndIf}

   # Recuperação de falha: crash → restart em 5s (3 vezes, reset diário).
   nsExec::ExecToLog /OEM '"$SYSDIR\sc.exe" failure ${DISCOVERY_SERVICE_NAME} reset= 86400 actions= restart/5000/restart/5000/restart/5000'
   Pop $R0

   nsExec::ExecToLog /OEM '"$SYSDIR\sc.exe" description ${DISCOVERY_SERVICE_NAME} "Discovery Agent core service (NATS, inventory, automation, P2P)"'
   Pop $R0

   nsExec::ExecToLog /OEM '"$SYSDIR\sc.exe" start ${DISCOVERY_SERVICE_NAME}'
   Pop $R0
   ${If} $R0 != 0
      ${InstallerLog} "Aviso: sc start ${DISCOVERY_SERVICE_NAME} falhou (codigo $R0) — serviço iniciara no proximo boot; UI segue via Task Scheduler"
   ${EndIf}

   Goto register_agent_svc_done
   register_agent_svc_skip:
      ${InstallerLog} "Aviso: $INSTDIR\${SERVICE_EXECUTABLE} ausente — serviço não registrado; agente seguirá em modo standalone via Task Scheduler"
   register_agent_svc_done:
FunctionEnd

# ── Health check e rollback do self-update (item 2) ──────────────────────

# VerifyAgentServiceHealth: depois de copiar os binários e registrar o
# serviço, espera até 2 minutos (A.2.4) por STATE=RUNNING. "RUNNING" isolado
# NÃO basta: um serviço que sobe e cai logo depois faria o instalador apagar o
# único backup (.bak_update) e reportar sucesso com o serviço morto. Por isso,
# depois do primeiro RUNNING, espera 5s e confirma que continua RUNNING antes
# de descartar o backup. Se não subir/estabilizar, restaura o binário anterior
# e reinicia o serviço (rollback), com exit code não-zero para o updater.
Function VerifyAgentServiceHealth
   IfFileExists "$INSTDIR\${SERVICE_EXECUTABLE}" 0 health_skip
   ${InstallerLog} "Health check: aguardando ${DISCOVERY_SERVICE_NAME} RUNNING..."
   StrCpy $R1 0
   health_retry:
      ${If} $R1 >= 120
         Goto health_failed
      ${EndIf}
      # Pipe precisa de cmd.exe (nsExec não interpreta pipes de shell).
      nsExec::ExecToLog /OEM '"$SYSDIR\cmd.exe" /c sc.exe query ${DISCOVERY_SERVICE_NAME} | "$SYSDIR\find.exe" "RUNNING"'
      Pop $R2
      ${If} $R2 == 0
         ${InstallerLog} "Health check: ${DISCOVERY_SERVICE_NAME} RUNNING — confirmando estabilidade (5s)..."
         Sleep 5000
         nsExec::ExecToLog /OEM '"$SYSDIR\cmd.exe" /c sc.exe query ${DISCOVERY_SERVICE_NAME} | "$SYSDIR\find.exe" "RUNNING"'
         Pop $R2
         ${If} $R2 != 0
            ${InstallerLogError} "Health check: servico caiu logo apos o RUNNING inicial (nao estabilizou)"
            Goto health_failed
         ${EndIf}
         ${InstallerLog} "Health check: ${DISCOVERY_SERVICE_NAME} RUNNING estavel"
         Delete "$INSTDIR\${SERVICE_EXECUTABLE}.bak_update"
         ${InstallerLog} "Cleanup: ${SERVICE_EXECUTABLE}.bak_update removido (health check ok)"
         Goto health_skip
      ${EndIf}
      ${If} $R1 == 3
      ${OrIf} $R1 == 15
         # Retentativas de start: o SCM pode ainda estar finalizando a criação
         # do serviço quando RegisterAgentService chamou sc start.
         nsExec::ExecToLog /OEM '"$SYSDIR\sc.exe" start ${DISCOVERY_SERVICE_NAME}'
         Pop $R0
      ${EndIf}
      Sleep 1000
      IntOp $R1 $R1 + 1
      Goto health_retry
   health_failed:
      ${InstallerLogError} "Health check FALHOU: ${DISCOVERY_SERVICE_NAME} nao ficou RUNNING estavel em 120s"
      ${If} $ServiceBackupCreated == "1"
         # Update in-place: existe binário anterior para restaurar.
         Call RollbackAgentServiceUpdate
      ${Else}
         # Instalação nova (sem backup): NÃO apaga a definição do serviço — as
         # failure actions e o start= auto ainda podem recuperá-lo no boot.
         ${InstallerLogError} "Health check: sem .bak_update (instalacao nova) — servico mantido para recuperacao no proximo boot"
         SetErrorLevel 1
      ${EndIf}
   health_skip:
FunctionEnd

# RollbackAgentServiceUpdate: remove a definição do serviço quebrado, restaura
# discovery-service.exe.bak_update e recria/inicia o serviço com os mesmos
# parâmetros de RegisterAgentService (binPath, failure actions, descrição).
Function RollbackAgentServiceUpdate
   # PRÉ-CONDIÇÃO: sem backup NÃO se apaga a definição do serviço. Antes o
   # sc delete vinha primeiro e, se o .bak_update tivesse desaparecido (AV,
   # limpeza, rollback anterior), a máquina ficava sem serviço nenhum — pior
   # que o binário novo com problema. Sem backup, a definição fica intacta e
   # as failure actions/start=auto ainda podem recuperar no boot.
   IfFileExists "$INSTDIR\${SERVICE_EXECUTABLE}.bak_update" 0 rollback_no_backup
   ${InstallerLogError} "Rollback do servico: restaurando ${SERVICE_EXECUTABLE}.bak_update..."
   nsExec::ExecToLog /OEM '"$SYSDIR\sc.exe" stop ${DISCOVERY_SERVICE_NAME}'
   Pop $R0
   nsExec::ExecToLog /OEM '"$SYSDIR\sc.exe" delete ${DISCOVERY_SERVICE_NAME}'
   Pop $R0
   Sleep 1000
   nsExec::ExecToLog /OEM '"$SYSDIR\taskkill.exe" /IM "${SERVICE_EXECUTABLE}" /F'
   Pop $R0

   # (Sem segundo check do backup: se ele desaparecer entre o check acima e o
   # Rename, o próprio Rename falha e o serviço é recriado logo abaixo — nunca
   # deixamos o SCM sem definição.)
   Delete "$INSTDIR\${SERVICE_EXECUTABLE}"
   ClearErrors
   Rename "$INSTDIR\${SERVICE_EXECUTABLE}.bak_update" "$INSTDIR\${SERVICE_EXECUTABLE}"
   ${If} ${Errors}
      ${InstallerLogError} "Rollback: FALHA ao restaurar ${SERVICE_EXECUTABLE}.bak_update — recriando a definicao do servico com o binario disponivel"
   ${Else}
      ${InstallerLog} "Rollback: binario anterior restaurado em $INSTDIR\${SERVICE_EXECUTABLE}"
   ${EndIf}

   # O sc delete anterior pode ainda estar em andamento ("marked for delete"):
   # sc create falha com 1072 nesse intervalo. Tenta por até 10s antes de
   # desistir, para não deixar o SCM sem a definição do serviço.
   StrCpy $R5 0
   rollback_create_retry:
      nsExec::ExecToLog /OEM '"$SYSDIR\sc.exe" create ${DISCOVERY_SERVICE_NAME} binPath= "\"$INSTDIR\${SERVICE_EXECUTABLE}\"" start= auto obj= LocalSystem DisplayName= "Discovery Agent Service"'
      Pop $R0
      ${If} $R0 != 0
         nsExec::ExecToLog /OEM '"$SYSDIR\sc.exe" config ${DISCOVERY_SERVICE_NAME} binPath= "\"$INSTDIR\${SERVICE_EXECUTABLE}\"" start= auto'
         Pop $R0
      ${EndIf}
      ${If} $R0 == 0
         Goto rollback_create_done
      ${EndIf}
      IntOp $R5 $R5 + 1
      ${If} $R5 >= 10
         ${InstallerLogError} "Rollback: sc create/config falhou apos 10 tentativas (codigo $R0)"
         Goto rollback_create_done
      ${EndIf}
      Sleep 1000
      Goto rollback_create_retry
   rollback_create_done:
   # Preserva as failure actions (crash → restart em 5s, 3 vezes, reset diário).
   nsExec::ExecToLog /OEM '"$SYSDIR\sc.exe" failure ${DISCOVERY_SERVICE_NAME} reset= 86400 actions= restart/5000/restart/5000/restart/5000'
   Pop $R0
   nsExec::ExecToLog /OEM '"$SYSDIR\sc.exe" description ${DISCOVERY_SERVICE_NAME} "Discovery Agent core service (NATS, inventory, automation, P2P)"'
   Pop $R0
   nsExec::ExecToLog /OEM '"$SYSDIR\sc.exe" start ${DISCOVERY_SERVICE_NAME}'
   Pop $R0
   ${If} $R0 != 0
      ${InstallerLogError} "Rollback: sc start do servico restaurado falhou (codigo $R0)"
   ${Else}
      ${InstallerLog} "Rollback concluido: ${SERVICE_EXECUTABLE} anterior reiniciado"
   ${EndIf}
   SetErrorLevel 1
   StrCpy $ServiceRollbackDone "1"
   Goto rollback_done

   rollback_no_backup:
      ${InstallerLogError} "Rollback: ${SERVICE_EXECUTABLE}.bak_update ausente — definicao do servico MANTIDA intacta para recuperacao no proximo boot"
      SetErrorLevel 1
      StrCpy $ServiceRollbackDone "1"
   rollback_done:
FunctionEnd

# RecoverAgentBinariesIfMissing: desfaz o rename para .bak_update quando o
# binário original NÃO chegou a ser copiado (abort/cancelamento/erro entre o
# backup e a cópia). Sem isso o $INSTDIR fica sem discovery-agent.exe ou
# discovery-service.exe e o sc start do .onInstFailed falha para sempre.
# Só restaura quando o destino está AUSENTE: se o binário novo já está no
# lugar, .bak_update é backup de rollback e permanece intacto.
Function RecoverAgentBinariesIfMissing
   ${InstallerLogInit}
   IfFileExists "$INSTDIR\${PRODUCT_EXECUTABLE}" 0 recover_ui
   Goto recover_service
   recover_ui:
   IfFileExists "$INSTDIR\${PRODUCT_EXECUTABLE}.bak_update" 0 recover_service
   ClearErrors
   Rename "$INSTDIR\${PRODUCT_EXECUTABLE}.bak_update" "$INSTDIR\${PRODUCT_EXECUTABLE}"
   ${If} ${Errors}
      ${InstallerLogError} "Recuperacao: falha ao restaurar ${PRODUCT_EXECUTABLE}.bak_update"
   ${Else}
      ${InstallerLog} "Recuperacao: ${PRODUCT_EXECUTABLE}.bak_update restaurado (abort durante o update)"
   ${EndIf}
   recover_service:
   IfFileExists "$INSTDIR\${SERVICE_EXECUTABLE}" 0 recover_svc
   Goto recover_done
   recover_svc:
   IfFileExists "$INSTDIR\${SERVICE_EXECUTABLE}.bak_update" 0 recover_done
   ClearErrors
   Rename "$INSTDIR\${SERVICE_EXECUTABLE}.bak_update" "$INSTDIR\${SERVICE_EXECUTABLE}"
   ${If} ${Errors}
      ${InstallerLogError} "Recuperacao: falha ao restaurar ${SERVICE_EXECUTABLE}.bak_update"
   ${Else}
      ${InstallerLog} "Recuperacao: ${SERVICE_EXECUTABLE}.bak_update restaurado (abort durante o update)"
   ${EndIf}
   recover_done:
FunctionEnd

# RecoverAgentServiceIfStopped: bloco de recuperação para abort de instalação
# DEPOIS do sc stop. Chamado por .onInstFailed; se o serviço existe e não está
# RUNNING, tenta reiniciá-lo para não deixar a máquina sem core.
Function RecoverAgentServiceIfStopped
   ${InstallerLogInit}
   nsExec::ExecToLog /OEM '"$SYSDIR\sc.exe" query ${DISCOVERY_SERVICE_NAME}'
   Pop $R2
   ${If} $R2 == 1060
      Return
   ${EndIf}
   nsExec::ExecToLog /OEM '"$SYSDIR\cmd.exe" /c sc.exe query ${DISCOVERY_SERVICE_NAME} | "$SYSDIR\find.exe" "RUNNING"'
   Pop $R2
   ${If} $R2 == 0
      Return
   ${EndIf}
   ${InstallerLog} "Recuperacao apos abort: ${DISCOVERY_SERVICE_NAME} nao esta RUNNING — tentando iniciar"
   nsExec::ExecToLog /OEM '"$SYSDIR\sc.exe" start ${DISCOVERY_SERVICE_NAME}'
   Pop $R0
   ${If} $R0 != 0
      ${InstallerLogError} "Recuperacao apos abort: sc start falhou (codigo $R0)"
   ${Else}
      ${InstallerLog} "Recuperacao apos abort: ${DISCOVERY_SERVICE_NAME} reiniciado"
   ${EndIf}
FunctionEnd

# un.UnregisterAgentService: para e remove o serviço no uninstall.
Function un.UnregisterAgentService
   DetailPrint "Removendo serviço ${DISCOVERY_SERVICE_NAME}..."
   Call un.StopAndWaitAgentService
   nsExec::ExecToLog /OEM '"$SYSDIR\sc.exe" delete ${DISCOVERY_SERVICE_NAME}'
   Pop $R0
FunctionEnd

# un.StopAndWaitAgentService: variante uninstaller de StopAndWaitAgentService.
Function un.StopAndWaitAgentService
   # Fast-path: serviço inexistente (sc query -> 1060) sai sem esperar.
   nsExec::ExecToLog /OEM '"$SYSDIR\sc.exe" query ${DISCOVERY_SERVICE_NAME}'
   Pop $R2
   ${If} $R2 == 1060
      Goto un_svc_wait_done
   ${EndIf}

   nsExec::ExecToLog /OEM '"$SYSDIR\sc.exe" stop ${DISCOVERY_SERVICE_NAME}'
   Pop $R0
   StrCpy $R1 0
   un_svc_wait_stopped:
      ${If} $R1 >= 30
         Goto un_svc_wait_done
      ${EndIf}
      # Pipe precisa de cmd.exe (nsExec não interpreta pipes de shell).
      nsExec::ExecToLog /OEM '"$SYSDIR\cmd.exe" /c sc.exe query ${DISCOVERY_SERVICE_NAME} | "$SYSDIR\find.exe" "STOPPED"'
      Pop $R2
      ${If} $R2 == 0
         Goto un_svc_wait_done
      ${EndIf}
      Sleep 1000
      IntOp $R1 $R1 + 1
      Goto un_svc_wait_stopped
   un_svc_wait_done:
FunctionEnd

# ── Help: exibe MessageBox com todos os parametros disponiveis e sai ──
Function ShowHelpMessageBox
   StrCpy $R9 "Discovery Agent Installer v${INFO_PRODUCTVERSION}"

   ; Identifica tipo de build no titulo
   !if "${BUILD_BOOTSTRAP_INSTALL}" == "1"
      StrCpy $R9 "$R9 (build: bootstrap)"
   !else if "${BUILD_UPDATE_INSTALL}" == "1"
      StrCpy $R9 "$R9 (build: update)"
   !else
      StrCpy $R9 "$R9 (build: standard)"
   !endif

   MessageBox MB_OK|MB_ICONINFORMATION "$R9$\r$\n\
$\r$\n\
Parametros disponiveis (forma longa / forma curta):$\r$\n\
$\r$\n\
  /?, /H, /HELP           Exibe esta ajuda$\r$\n\
  /S                      Instalacao silenciosa (sem interface grafica)$\r$\n\
  /UPDATE, /U             Modo atualizacao: silencioso, preserva config local$\r$\n\
  /URL=<endereco>         URL do servidor API$\r$\n\
  /KEY=<chave>, /K=<c>    Chave de API / tenant key$\r$\n\
  /AUTO_PROVISIONING=<0|1>, /AP=<0|1>$\r$\n\
                          Habilita (1) ou desabilita (0) zero-touch provisioning$\r$\n\
  /ALLOW_INSECURE_TLS=<1|true|yes|on>, /TLS=<valor>$\r$\n\
                          Permite conexao TLS insegura$\r$\n\
  /MINIMAL, /M            Modo minimo: pula wizard se URL+KEY informados$\r$\n\
  /GENERIC, /G            Modo generico: provisioning P2P sem wizard$\r$\n\
  /D=<diretorio>          Diretorio de instalacao$\r$\n\
                          (padrao: C:\Program Files\Discovery)$\r$\n\
$\r$\n\
Exemplos:$\r$\n\
$\r$\n\
  discovery-amd64-installer.exe /S /URL=api.empresa.com /KEY=abc123$\r$\n\
  discovery-amd64-installer.exe /U$\r$\n\
  discovery-amd64-installer.exe /G /AP=1$\r$\n\
  discovery-amd64-installer.exe /S /URL=api.empresa.com /K=abc123 /D=C:\Discovery$\r$\n\
$\r$\n\
Documentacao: https://docs.discovery.rmm$\r$\n\
Para ajuda do agente: discovery-agent.exe --help (ou /?)"
FunctionEnd


