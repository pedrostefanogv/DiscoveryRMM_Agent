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
!define LEGACY_PRODUCT_EXECUTABLE "discovery.exe"
!define UNINST_KEY_NAME     "Discovery.RMM"
!define DISCOVERY_UI_TASK_NAME "DiscoveryAgentUI"
!define DISCOVERY_FIREWALL_RULE_NAME "Discovery Agent Network Access"

!ifndef ARG_WAILS_AMD64_BINARY
!ifdef WAILS_AMD64_BINARY
!define ARG_WAILS_AMD64_BINARY "${WAILS_AMD64_BINARY}"
!endif
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
         FileWrite $R9 "CommandLine: $CMDLINE$\r$\n"
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
         ; Se a cópia falhou, tentar restaurar o .bak
         IfFileExists "$INSTDIR\${PRODUCT_EXECUTABLE}.bak_update" 0 install_binary_ok
         Rename "$INSTDIR\${PRODUCT_EXECUTABLE}.bak_update" "$INSTDIR\${PRODUCT_EXECUTABLE}"
         ${InstallerLogError} "Restaurado .bak_update -> ${PRODUCT_EXECUTABLE} (rollback)"
         install_binary_ok:

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

         # Modo de execucao: apenas Task Scheduler (tray icon no logon de qualquer usuario).
         # Nao usamos mais Windows Service mode.

         # Registrar autostart da UI via Task Scheduler (At log on of any user)
         Call RegisterUIStartupTask

         !insertmacro wails.associateFiles
         !insertmacro wails.associateCustomProtocols

         !insertmacro wails.writeUninstaller
         WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${UNINST_KEY_NAME}" "InstallLocation" "$INSTDIR"

         ; Restart agent apos update silencioso (/UPDATE).
         ; O Task Scheduler so dispara no login; restart imediato garante que o
         ; agente volte ao ar assim que o instalador terminar.
         ${If} $UpdateMode == "1"
            ${InstallerLog} "Update mode: verificando binario antes de reiniciar..."
            IfFileExists "$INSTDIR\${PRODUCT_EXECUTABLE}" 0 update_binary_missing
            ${InstallerLog} "Update mode: binario encontrado — reiniciando agente..."
            Exec '"$INSTDIR\${PRODUCT_EXECUTABLE}"'
            Goto update_restart_done
            update_binary_missing:
            ${InstallerLogError} "Update mode: binario NAO encontrado em $INSTDIR\${PRODUCT_EXECUTABLE} — agente nao reiniciado"
            update_restart_done:
         ${EndIf}
         ${InstallerLog} "Instalacao concluida com sucesso"
      !endif
SectionEnd

Section "uninstall"
    !insertmacro wails.setShellContext

   # Encerrar processo antes de limpar binarios
   Call un.UnregisterWindowsFirewallRule

   # Garantir encerramento de qualquer instancia em modo UI/headless.
   nsExec::ExecToLog /OEM '"$SYSDIR\taskkill.exe" /IM "${PRODUCT_EXECUTABLE}" /F /T'
   Pop $R0
   ${If} $R0 != 0
      DetailPrint "Aviso: taskkill retornou codigo $R0 (pode nao haver processo em execucao)."
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

   # Remover task agendada de autostart da UI
   Call un.UnregisterUIStartupTask

    RMDir /r "$AppData\${PRODUCT_EXECUTABLE}" # Remove the WebView2 DataPath
   ${If} "${LEGACY_PRODUCT_EXECUTABLE}" != "${PRODUCT_EXECUTABLE}"
      RMDir /r "$AppData\${LEGACY_PRODUCT_EXECUTABLE}"
   ${EndIf}

   ReadEnvStr $R2 "ProgramData"
   ${If} $R2 != ""
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
   StrCpy $R0 "$INSTDIR\${PRODUCT_EXECUTABLE}"
   ${If} ${FileExists} "$R0"
      DetailPrint "Abrindo agente em $R0"
      Exec '"$R0"'
      Return
   ${EndIf}

   # Fallback: usar caminho registrado pelo instalador completo.
   ReadRegStr $R1 HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${UNINST_KEY_NAME}" "InstallLocation"
   ${If} $R1 != ""
      StrCpy $R0 "$R1\${PRODUCT_EXECUTABLE}"
      ${If} ${FileExists} "$R0"
         DetailPrint "Abrindo agente em $R0"
         Exec '"$R0"'
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

   StrCpy $R6 "$TEMP\DiscoveryBootstrap"
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
         DetailPrint "Aviso: nao foi possivel validar integridade (SHA256 endpoint indisponivel). Prosseguindo..."
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

   ; Tentar remover startup task antiga antes de atualizar binarios.
   ${InstallerLog} "Removendo startup task antiga..."
   Call UnregisterUIStartupTask

   # Garantir que nenhuma instancia do app permaneceu em execucao.
   nsExec::ExecToLog /OEM '"$SYSDIR\taskkill.exe" /IM "${PRODUCT_EXECUTABLE}" /F /T'
   Pop $R0
   ${If} $R0 != 0
      ${InstallerLog} "taskkill ${PRODUCT_EXECUTABLE}: codigo $R0 (pode nao haver processo)"
   ${Else}
      ${InstallerLog} "taskkill ${PRODUCT_EXECUTABLE}: processo encerrado"
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
   StrCpy $R3 0
   retry_lock_check:
      ${If} $R3 >= 5
         ${InstallerLogError} "lock check: falhou apos 5 tentativas — prosseguindo mesmo assim"
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
FunctionEnd

Function RegisterWindowsFirewallRule
   DetailPrint "Registrando regra de Windows Firewall para ${PRODUCT_EXECUTABLE}"

   # netsh advfirewall: nativo do Windows, sem dependencia de PowerShell ou plugins.
   # Remove regra anterior (idempotente) e cria nova.
   nsExec::ExecToLog /OEM '"$SYSDIR\netsh.exe" advfirewall firewall delete rule name="${DISCOVERY_FIREWALL_RULE_NAME}"'
   Pop $R0

   nsExec::ExecToLog /OEM '"$SYSDIR\netsh.exe" advfirewall firewall add rule name="${DISCOVERY_FIREWALL_RULE_NAME}" dir=in action=allow program="$INSTDIR\${PRODUCT_EXECUTABLE}" enable=yes profile=any'
   Pop $R0

   ${If} $R0 != 0
      DetailPrint "Aviso: falha ao registrar regra de Windows Firewall para ${PRODUCT_EXECUTABLE}. Codigo: $R0"
   ${EndIf}
FunctionEnd

Function RegisterUIStartupTask
   DetailPrint "Registrando tarefa de autostart da UI (${DISCOVERY_UI_TASK_NAME})"

   ; Limpar atalho legado de startup (migracao)
   Delete "$SMSTARTUP\${INFO_PRODUCTNAME}.lnk"

   ; Evita fragilidade de quoting no -Command inline: gera script temporario.
   StrCpy $R9 "$TEMP\discovery_ui_task_reg.ps1"
   ClearErrors
   FileOpen $R8 "$R9" w
   ${If} ${Errors}
      Goto ui_startup_fallback
   ${EndIf}

   FileWrite $R8 "try {$\r$\n"
   FileWrite $R8 "  $$action = New-ScheduledTaskAction -Execute '$INSTDIR\${PRODUCT_EXECUTABLE}' -Argument '--startup-minimized --startup-source=task-scheduler'$\r$\n"
   FileWrite $R8 "  $$trigger = New-ScheduledTaskTrigger -AtLogOn$\r$\n"
   FileWrite $R8 "  $$trigger.Delay = 'PT30S'$\r$\n"
   FileWrite $R8 "  $$principal = New-ScheduledTaskPrincipal -GroupId 'S-1-5-32-545' -RunLevel Limited$\r$\n"
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
   CreateShortCut "$SMSTARTUP\${INFO_PRODUCTNAME}.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}" "--startup-minimized --startup-source=startup-link" "$INSTDIR\${PRODUCT_EXECUTABLE}" 0
   ${If} ${Errors}
      MessageBox MB_ICONSTOP "Falha ao registrar autostart da UI (${DISCOVERY_UI_TASK_NAME}) em Scheduled Task e Startup folder."
      Abort
   ${EndIf}

   DetailPrint "Fallback aplicado: autostart da UI registrado via Startup folder."
FunctionEnd

Function un.UnregisterWindowsFirewallRule
   DetailPrint "Removendo regra de Windows Firewall de ${PRODUCT_EXECUTABLE}"

   # netsh advfirewall: nativo do Windows, sem PowerShell.
   nsExec::ExecToLog /OEM '"$SYSDIR\netsh.exe" advfirewall firewall delete rule name="${DISCOVERY_FIREWALL_RULE_NAME}"'
   Pop $R0

   ${If} $R0 != 0
      DetailPrint "Aviso: falha ao remover regra de Windows Firewall de ${PRODUCT_EXECUTABLE}. Codigo: $R0"
   ${EndIf}
FunctionEnd

Function UnregisterUIStartupTask
   DetailPrint "Removendo tarefa de autostart da UI (${DISCOVERY_UI_TASK_NAME})"
   nsExec::ExecToLog /OEM '"$SYSDIR\schtasks.exe" /Delete /TN "${DISCOVERY_UI_TASK_NAME}" /F'
   Pop $R0
   Delete "$SMSTARTUP\${INFO_PRODUCTNAME}.lnk"
FunctionEnd

Function un.UnregisterUIStartupTask
   DetailPrint "Removendo tarefa de autostart da UI (${DISCOVERY_UI_TASK_NAME})"
   nsExec::ExecToLog /OEM '"$SYSDIR\schtasks.exe" /Delete /TN "${DISCOVERY_UI_TASK_NAME}" /F'
   Pop $R0
   Delete "$SMSTARTUP\${INFO_PRODUCTNAME}.lnk"
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


