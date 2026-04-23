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
!define INFO_PRODUCTVERSION "1.0.0"
!endif
!define INFO_COPYRIGHT      "Copyright (c) 2026 Discovery"
!define PRODUCT_EXECUTABLE  "discovery.exe"
!define UNINST_KEY_NAME     "Discovery.RMM"
!define DISCOVERY_SERVICE_NAME "DiscoveryAgent"
!define DISCOVERY_UI_TASK_NAME "DiscoveryAgentUI"

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
VIProductVersion "${INFO_PRODUCTVERSION}.0"
VIFileVersion    "${INFO_PRODUCTVERSION}.0"

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
Var DiscoveryEnabled
Var MinimalMode
Var GenericMode
Var PayloadUrl
Var PayloadSha256
Var PayloadFileName

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
InstallDir "$PROGRAMFILES64\${INFO_COMPANYNAME}\${INFO_PRODUCTNAME}" # Default installing folder ($PROGRAMFILES is Program Files folder).
ShowInstDetails show # This will always show the installation details.

Function .onInit
   !insertmacro wails.checkArchitecture
   
   # Definir valores padrão vindos do build
   StrCpy $ServerUrl "${BUILD_DEFAULT_URL}"
   StrCpy $ServerKey "${BUILD_DEFAULT_KEY}"
   StrCpy $DiscoveryEnabled "${BUILD_DEFAULT_DISCOVERY}"
   StrCpy $MinimalMode "${BUILD_DEFAULT_MINIMAL}"
   StrCpy $GenericMode "${BUILD_GENERIC_INSTALL}"
   StrCpy $PayloadUrl "${BUILD_PAYLOAD_URL}"
   StrCpy $PayloadSha256 "${BUILD_PAYLOAD_SHA256}"
   StrCpy $PayloadFileName "${BUILD_PAYLOAD_FILENAME}"

   # Build de update: modo silencioso por padrão e sem wizard.
   ${If} "${BUILD_UPDATE_INSTALL}" == "1"
      StrCpy $MinimalMode "1"
      SetSilent silent
   ${EndIf}

   # Normalizar defaults inválidos
   ${If} $DiscoveryEnabled != "0"
   ${AndIf} $DiscoveryEnabled != "1"
      StrCpy $DiscoveryEnabled "1"
   ${EndIf}

   ${If} $MinimalMode != "0"
   ${AndIf} $MinimalMode != "1"
      StrCpy $MinimalMode "0"
   ${EndIf}
   
   # Obter parâmetros da linha de comando
   ${GetParameters} $R0
   
   # Parse URL
   ${GetOptions} $R0 "/URL=" $R1
   ${If} $R1 != ""
      StrCpy $ServerUrl $R1
   ${EndIf}
   
   # Parse KEY
   ${GetOptions} $R0 "/KEY=" $R1
   ${If} $R1 != ""
      StrCpy $ServerKey $R1
   ${EndIf}

   # Parse PAYLOAD_URL (override de build para bootstrap)
   ${GetOptions} $R0 "/PAYLOAD_URL=" $R1
   ${If} $R1 != ""
      StrCpy $PayloadUrl $R1
   ${EndIf}
   
   # Parse DISCOVERY (0 ou 1)
   ${GetOptions} $R0 "/DISCOVERY=" $R1
   ${If} $R1 != ""
      ${If} $R1 == "0"
      ${OrIf} $R1 == "1"
         StrCpy $DiscoveryEnabled $R1
      ${EndIf}
   ${EndIf}
   
   # Verificar modo mínimo
   ${GetOptions} $R0 "/MINIMAL" $R1
   ${IfNot} ${Errors}
      StrCpy $MinimalMode "1"
      # Em modo mínimo, pular wizard se tiver parâmetros
      ${If} $ServerUrl != ""
      ${AndIf} $ServerKey != ""
         SetSilent normal  # Mostra só o progresso
      ${EndIf}
   ${EndIf}

   # Modo genérico: sem URL/KEY — agente entra em auto-provisioning via P2P
   ${GetOptions} $R0 "/GENERIC" $R1
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
   
   # Checkbox para habilitar/desabilitar Discovery
   ${NSD_CreateCheckbox} 0 90u 100% 12u "Habilitar Discovery (monitoramento automático)"
   Pop $DiscoveryCheck
   ${If} $DiscoveryEnabled == "1"
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
      StrCpy $DiscoveryEnabled "1"
   ${Else}
      StrCpy $DiscoveryEnabled "0"
   ${EndIf}
FunctionEnd

Section
    !insertmacro wails.setShellContext

      SetOutPath $INSTDIR

      !if "${BUILD_BOOTSTRAP_INSTALL}" == "1"
         # Bootstrapper: grava config local e dispara instalador completo de segunda etapa.
         Call EnsureSharedDataDir
         Call SaveAgentConfig
         Call DownloadAndRunStage2
      !else
         !insertmacro wails.webview2runtime
         !insertmacro wails.files

         CreateShortcut "$SMPROGRAMS\${INFO_PRODUCTNAME}.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}"
         CreateShortCut "$DESKTOP\${INFO_PRODUCTNAME}.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}"

         # Garantir estrutura compartilhada em ProgramData
         Call EnsureSharedDataDir

         # Salvar configurações do agente
         Call SaveAgentConfig

         # Registrar e iniciar serviço Windows (modo headless 24/7)
         Call RegisterWindowsService

         # Registrar autostart da UI via Task Scheduler (At log on of any user)
         Call RegisterUIStartupTask

         !insertmacro wails.associateFiles
         !insertmacro wails.associateCustomProtocols

         !insertmacro wails.writeUninstaller
      !endif
SectionEnd

Section "uninstall"
    !insertmacro wails.setShellContext

   # Encerrar/remover service antes de limpar binários
   Call un.UnregisterWindowsService

   # Remover task agendada de autostart da UI
   Call un.UnregisterUIStartupTask

    RMDir /r "$AppData\${PRODUCT_EXECUTABLE}" # Remove the WebView2 DataPath

    RMDir /r $INSTDIR

    Delete "$SMPROGRAMS\${INFO_PRODUCTNAME}.lnk"
    Delete "$DESKTOP\${INFO_PRODUCTNAME}.lnk"
   Delete "$SMSTARTUP\${INFO_PRODUCTNAME}.lnk"

    !insertmacro wails.unassociateFiles
    !insertmacro wails.unassociateCustomProtocols

    !insertmacro wails.deleteUninstaller
SectionEnd

# Função para salvar as configurações do agente
Function SaveAgentConfig
   # Build de update não altera configuração local existente.
   ${If} "${BUILD_UPDATE_INSTALL}" == "1"
      DetailPrint "Update build: mantendo configuração existente sem sobrescrita"
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

   Delete "$INSTDIR\config.json"
   ClearErrors
   IfFileExists "$R0\Discovery\config.json" 0 +3
   StrCpy $R1 "$R0\Discovery\installer.json"
   Goto +2
   StrCpy $R1 "$R0\Discovery\config.json"
   DetailPrint "Salvando config em $R1"
   FileOpen $0 "$R1" w
   ${If} ${Errors}
      DetailPrint "ERRO: nao foi possivel abrir $R0\Discovery\config.json para escrita"
      MessageBox MB_ICONSTOP "Falha ao gravar o arquivo de configuracao em $R0\Discovery\config.json"
      Abort
   ${EndIf}
   
   FileWrite $0 "{$\r$\n"
   ${If} $GenericMode == "1"
      ; Modo genérico: sem URL/KEY, apenas habilita P2P para auto-provisioning
      FileWrite $0 '  "inventory_sync_interval_minutes": 15,$\r$\n'
      FileWrite $0 '  "discoveryEnabled": true,$\r$\n'
      FileWrite $0 '  "p2p_enabled": true$\r$\n'
   ${Else}
      ; Modo padrão: escreve URL, KEY e flags conforme configuração
      FileWrite $0 '  "server_url": "$ServerUrl",$\r$\n'
      FileWrite $0 '  "api_scheme": "https",$\r$\n'
      FileWrite $0 '  "api_server": "$ServerUrl",$\r$\n'
      FileWrite $0 '  "inventory_sync_interval_minutes": 15,$\r$\n'
      FileWrite $0 '  "serverUrl": "$ServerUrl",$\r$\n'
      FileWrite $0 '  "apiKey": "$ServerKey",$\r$\n'
      ${If} $DiscoveryEnabled == "1"
         FileWrite $0 '  "discoveryEnabled": true,$\r$\n'
         FileWrite $0 '  "p2p_enabled": true$\r$\n'
      ${Else}
         FileWrite $0 '  "discoveryEnabled": false,$\r$\n'
         FileWrite $0 '  "p2p_enabled": false$\r$\n'
      ${EndIf}
   ${EndIf}
   FileWrite $0 "}$\r$\n"
   
   FileClose $0
   DetailPrint "Config salvo com sucesso em $R0\Discovery\config.json"
FunctionEnd

Function LaunchInstalledApp
   ${If} "${BUILD_BOOTSTRAP_INSTALL}" == "1"
      Return
   ${EndIf}
   Exec '"$INSTDIR\${PRODUCT_EXECUTABLE}"'
FunctionEnd

Function DownloadAndRunStage2
   ${If} $PayloadUrl == ""
      MessageBox MB_ICONSTOP "Payload URL nao configurada no bootstrapper."
      Abort
   ${EndIf}

   ${If} $PayloadFileName == ""
      StrCpy $PayloadFileName "discovery-stage2-installer.exe"
   ${EndIf}

   StrCpy $R6 "$TEMP\DiscoveryBootstrap"
   CreateDirectory "$R6"
   StrCpy $R7 "$R6\$PayloadFileName"

   DetailPrint "Baixando instalador completo de segunda etapa..."

   StrCpy $R9 "$TEMP\discovery_stage2_download.ps1"
   FileOpen $R8 "$R9" w
   FileWrite $R8 "param([string]$$Url,[string]$$Out)$\r$\n"
   FileWrite $R8 "$$ErrorActionPreference = 'Stop'$\r$\n"
   FileWrite $R8 "Invoke-WebRequest -Uri $$Url -OutFile $$Out -UseBasicParsing$\r$\n"
   FileClose $R8

   ExecWait '"$SYSDIR\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -ExecutionPolicy Bypass -File "$R9" -Url "$PayloadUrl" -Out "$R7"' $R0
   Delete "$R9"

   ${If} $R0 != 0
      MessageBox MB_ICONSTOP "Falha ao baixar instalador completo (codigo: $R0)."
      Abort
   ${EndIf}

   ${If} $PayloadSha256 != ""
      DetailPrint "Validando integridade SHA256 do payload..."
      StrCpy $R9 "$TEMP\discovery_stage2_verify.ps1"
      FileOpen $R8 "$R9" w
      FileWrite $R8 "param([string]$$Path,[string]$$Expected)$\r$\n"
      FileWrite $R8 "$$ErrorActionPreference = 'Stop'$\r$\n"
      FileWrite $R8 "$$actual = (Get-FileHash -Algorithm SHA256 -Path $$Path).Hash.ToLowerInvariant()$\r$\n"
      FileWrite $R8 "$$expectedNorm = $$Expected.ToLowerInvariant()$\r$\n"
      FileWrite $R8 "if ($$actual -ne $$expectedNorm) { Write-Output ('sha256 mismatch: ' + $$actual); exit 2 }$\r$\n"
      FileClose $R8

      ExecWait '"$SYSDIR\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -ExecutionPolicy Bypass -File "$R9" -Path "$R7" -Expected "$PayloadSha256"' $R0
      Delete "$R9"

      ${If} $R0 != 0
         MessageBox MB_ICONSTOP "Falha na validacao de integridade do payload (codigo: $R0)."
         Abort
      ${EndIf}
   ${EndIf}

   DetailPrint "Executando instalador completo de segunda etapa..."
   ${If} $GenericMode == "1"
      ExecWait '"$R7" /S /GENERIC /DISCOVERY=$DiscoveryEnabled /MINIMAL' $R0
   ${Else}
      ExecWait '"$R7" /S /URL="$ServerUrl" /KEY="$ServerKey" /DISCOVERY=$DiscoveryEnabled /MINIMAL' $R0
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
FunctionEnd

Function RegisterWindowsService
   DetailPrint "Registrando Windows Service ${DISCOVERY_SERVICE_NAME}"

   # Remover versão anterior (idempotente)
   ExecWait '"$SYSDIR\sc.exe" stop "${DISCOVERY_SERVICE_NAME}"' $R0
   ExecWait '"$SYSDIR\sc.exe" delete "${DISCOVERY_SERVICE_NAME}"' $R0

   # Registrar serviço com inicialização automática
   ExecWait '"$SYSDIR\sc.exe" create "${DISCOVERY_SERVICE_NAME}" binPath= "\"$INSTDIR\${PRODUCT_EXECUTABLE}\" --service" start= auto DisplayName= "Discovery Agent Service"' $R0
   ${If} $R0 != 0
      MessageBox MB_ICONSTOP "Falha ao registrar o Windows Service (${DISCOVERY_SERVICE_NAME}). Codigo: $R0"
      Abort
   ${EndIf}

   # Configurar recuperação automática
   ExecWait '"$SYSDIR\sc.exe" failure "${DISCOVERY_SERVICE_NAME}" reset= 86400 actions= restart/5000/restart/5000/restart/5000' $R1
   ExecWait '"$SYSDIR\sc.exe" description "${DISCOVERY_SERVICE_NAME}" "Discovery background service (multi-user)"' $R1

   # Iniciar serviço após instalação
   ExecWait '"$SYSDIR\sc.exe" start "${DISCOVERY_SERVICE_NAME}"' $R1
   ${If} $R1 != 0
      DetailPrint "Aviso: service instalado, mas falhou ao iniciar automaticamente. Codigo: $R1"
   ${EndIf}
FunctionEnd

Function RegisterUIStartupTask
   DetailPrint "Registrando tarefa de autostart da UI (${DISCOVERY_UI_TASK_NAME})"

   ; Limpar atalho legado de startup (migracao)
   Delete "$SMSTARTUP\${INFO_PRODUCTNAME}.lnk"

   ; Escreve script PS1 temporário para evitar problemas de escaping no NSIS
   StrCpy $R9 "$TEMP\discovery_ui_task_reg.ps1"
   FileOpen $R8 "$R9" w
   FileWrite $R8 "try {$\r$\n"
   FileWrite $R8 "  $$action = New-ScheduledTaskAction -Execute '$INSTDIR\${PRODUCT_EXECUTABLE}' -Argument '--startup-minimized --startup-source=task-scheduler'$\r$\n"
   FileWrite $R8 "  $$trigger = New-ScheduledTaskTrigger -AtLogOn$\r$\n"
   FileWrite $R8 "  $$trigger.Delay = 'PT30S'$\r$\n"
   FileWrite $R8 "  $$principal = New-ScheduledTaskPrincipal -GroupId 'S-1-5-32-545' -LogonType Interactive -RunLevel Limited$\r$\n"
   FileWrite $R8 "  Register-ScheduledTask -TaskName '${DISCOVERY_UI_TASK_NAME}' -Action $$action -Trigger $$trigger -Principal $$principal -Description 'Discovery UI autostart for any logged-on user' -Force | Out-Null$\r$\n"
   FileWrite $R8 "  exit 0$\r$\n"
   FileWrite $R8 "} catch {$\r$\n"
   FileWrite $R8 "  Write-Output $$_.Exception.Message$\r$\n"
   FileWrite $R8 "  exit 1$\r$\n"
   FileWrite $R8 "}$\r$\n"
   FileClose $R8

   ExecWait '"$SYSDIR\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -ExecutionPolicy Bypass -File "$R9"' $R0
   Delete "$R9"

   ${If} $R0 != 0
      MessageBox MB_ICONSTOP "Falha ao registrar tarefa de autostart da UI (${DISCOVERY_UI_TASK_NAME}). Codigo: $R0"
      Abort
   ${EndIf}
FunctionEnd

Function UnregisterWindowsService
   DetailPrint "Removendo Windows Service ${DISCOVERY_SERVICE_NAME}"
   ExecWait '"$SYSDIR\sc.exe" stop "${DISCOVERY_SERVICE_NAME}"' $R0
   ExecWait '"$SYSDIR\sc.exe" delete "${DISCOVERY_SERVICE_NAME}"' $R0
FunctionEnd

Function UnregisterUIStartupTask
   DetailPrint "Removendo tarefa de autostart da UI (${DISCOVERY_UI_TASK_NAME})"
   ExecWait '"$SYSDIR\schtasks.exe" /Delete /TN "${DISCOVERY_UI_TASK_NAME}" /F' $R0
   Delete "$SMSTARTUP\${INFO_PRODUCTNAME}.lnk"
FunctionEnd

Function un.UnregisterWindowsService
   DetailPrint "Removendo Windows Service ${DISCOVERY_SERVICE_NAME}"
   ExecWait '"$SYSDIR\sc.exe" stop "${DISCOVERY_SERVICE_NAME}"' $R0
   ExecWait '"$SYSDIR\sc.exe" delete "${DISCOVERY_SERVICE_NAME}"' $R0
FunctionEnd

Function un.UnregisterUIStartupTask
   DetailPrint "Removendo tarefa de autostart da UI (${DISCOVERY_UI_TASK_NAME})"
   ExecWait '"$SYSDIR\schtasks.exe" /Delete /TN "${DISCOVERY_UI_TASK_NAME}" /F' $R0
   Delete "$SMSTARTUP\${INFO_PRODUCTNAME}.lnk"
FunctionEnd
