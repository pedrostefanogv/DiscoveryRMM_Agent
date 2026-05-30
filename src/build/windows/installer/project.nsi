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
!ifndef INFO_FILEVERSION
!define INFO_FILEVERSION "1.0.0.0"
!endif
!define INFO_COPYRIGHT      "Copyright (c) 2026 Discovery"
!define PRODUCT_EXECUTABLE  "discovery-agent.exe"
!define LEGACY_PRODUCT_EXECUTABLE "discovery.exe"
!define UNINST_KEY_NAME     "Discovery.RMM"
!define DISCOVERY_SERVICE_NAME "DiscoveryAgent"
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

!ifdef ARG_ENABLE_WINDOWS_SERVICE
!define BUILD_ENABLE_WINDOWS_SERVICE "${ARG_ENABLE_WINDOWS_SERVICE}"
!else
!ifdef ENABLE_WINDOWS_SERVICE
!define BUILD_ENABLE_WINDOWS_SERVICE "1"
!else
!define BUILD_ENABLE_WINDOWS_SERVICE "0"
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

!define MUI_ICON "..\icon.ico"
!define MUI_UNICON "..\icon.ico"
# !define MUI_WELCOMEFINISHPAGE_BITMAP "resources\leftimage.bmp" #Include this to add a bitmap on the left side of the Welcome Page. Must be a size of 164x314
!define MUI_FINISHPAGE_NOAUTOCLOSE # Wait on the INSTFILES page so the user can take a look into the details of the installation steps
!define MUI_FINISHPAGE_RUN
!define MUI_FINISHPAGE_RUN_TEXT "Abrir ${INFO_PRODUCTNAME} agora"
!define MUI_FINISHPAGE_RUN_FUNCTION LaunchInstalledApp
!define MUI_ABORTWARNING # This will warn the user if they exit from the installer.

# VariÃ¡veis para armazenar as configuraÃ§Ãµes
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

!insertmacro MUI_PAGE_WELCOME # Welcome to the installer page.
# !insertmacro MUI_PAGE_LICENSE "resources\eula.txt" # Adds a EULA page to the installer
!insertmacro MUI_PAGE_DIRECTORY # In which folder install page.

# PÃ¡gina customizada para configuraÃ§Ãµes do agente
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
   
   # Definir valores padrÃ£o vindos do build
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

   # Build de update: modo silencioso por padrÃ£o e sem wizard.
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

   # Normalizar defaults invÃ¡lidos
   ${If} $AutoProvisioning != "0"
   ${AndIf} $AutoProvisioning != "1"
      StrCpy $AutoProvisioning "1"
   ${EndIf}

   ${If} $MinimalMode != "0"
   ${AndIf} $MinimalMode != "1"
      StrCpy $MinimalMode "0"
   ${EndIf}
   
   # Obter parÃ¢metros da linha de comando
   ${GetParameters} $R0

   # Parse UPDATE (atualizacao in-place): silencioso, sem wizard e sem sobrescrever config.
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
   
   # Parse AUTO_PROVISIONING (0 ou 1) - chave canÃ´nica para zero-touch provisioning.
   # Aceita /DISCOVERY= como alias legado para compatibilidade com automaÃ§Ãµes antigas.
   ${GetOptions} $R0 "/AUTO_PROVISIONING=" $R1
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
   # Flag /BOOTSTRAP_STAGE2: chamado internamente pelo bootstrapper.
   # Forca silencio absoluto (sem janela, sem progresso, sem wizard) para
   # evitar duas telas de instalacao sobrepostas (bootstrap + stage2).
   # Deve ser processado ANTES do /MINIMAL para ter precedencia.
   ${GetOptions} $R0 "/BOOTSTRAP_STAGE2" $R1
   ${IfNot} ${Errors}
      SetSilent silent
      StrCpy $MinimalMode "1"
   ${EndIf}   
   # Verificar modo mÃ­nimo
   ${GetOptions} $R0 "/MINIMAL" $R1
   ${IfNot} ${Errors}
      StrCpy $MinimalMode "1"
      # Em modo mÃ­nimo, pular wizard se tiver parÃ¢metros
      ${If} $ServerUrl != ""
      ${AndIf} $ServerKey != ""
         SetSilent normal  # Mostra sÃ³ o progresso
      ${EndIf}
   ${EndIf}

   # Modo genÃ©rico: sem URL/KEY â€” agente entra em auto-provisioning via P2P
   ${GetOptions} $R0 "/GENERIC" $R1
   ${IfNot} ${Errors}
      StrCpy $GenericMode "1"
   ${EndIf}
FunctionEnd

# FunÃ§Ã£o para criar a pÃ¡gina de configuraÃ§Ã£o do agente
Function AgentConfigPage
   # Modo genÃ©rico: sem wizard, agente entra em auto-provisioning via P2P
   ${If} $GenericMode == "1"
      Abort
   ${EndIf}

   # Pular a pÃ¡gina se estiver em modo silencioso ou mÃ­nimo
   ${If} ${Silent}
   ${OrIf} $MinimalMode == "1"
      Abort
   ${EndIf}
   
   # Se jÃ¡ temos URL e KEY via CLI, pular o wizard
   ${If} $ServerUrl != ""
   ${AndIf} $ServerKey != ""
      Abort
   ${EndIf}
   
   nsDialogs::Create 1018
   Pop $Dialog
   ${If} $Dialog == error
      Abort
   ${EndIf}
   
   # TÃ­tulo da pÃ¡gina
   !insertmacro MUI_HEADER_TEXT "ConfiguraÃ§Ã£o do Agente Discovery" "Configure a conexÃ£o com o servidor"
   
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

# FunÃ§Ã£o para capturar os valores da pÃ¡gina customizada
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

      !if "${BUILD_BOOTSTRAP_INSTALL}" == "1"
         # Bootstrapper: grava config local e dispara instalador completo de segunda etapa.
         Call EnsureSharedDataDir
         Call SaveAgentConfig
         Call DownloadAndRunStage2
      !else
         # Atualizacao in-place: parar instancias anteriores para evitar lock no executavel.
         Call PrepareForInPlaceUpdate

         !insertmacro wails.webview2runtime
         !insertmacro wails.files

         ${If} "${LEGACY_PRODUCT_EXECUTABLE}" != "${PRODUCT_EXECUTABLE}"
            Delete "$INSTDIR\${LEGACY_PRODUCT_EXECUTABLE}"
         ${EndIf}

         CreateShortcut "$SMPROGRAMS\${INFO_PRODUCTNAME}.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}" "" "$INSTDIR\${PRODUCT_EXECUTABLE}" 0
         CreateShortCut "$DESKTOP\${INFO_PRODUCTNAME}.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}" "" "$INSTDIR\${PRODUCT_EXECUTABLE}" 0

         # Garantir estrutura compartilhada em ProgramData
         Call EnsureSharedDataDir

         # Salvar configuraÃ§Ãµes do agente
         Call SaveAgentConfig

         # Registrar regra de firewall para runtime local/P2P.
         Call RegisterWindowsFirewallRule

         # Opcional: registrar serviÃ§o Windows somente quando habilitado em build.
         ${If} "${BUILD_ENABLE_WINDOWS_SERVICE}" == "1"
            Call RegisterWindowsService
         ${Else}
            # Garantir migraÃ§Ã£o limpa removendo serviÃ§o legado, se existir.
            Call UnregisterWindowsService
         ${EndIf}

         # Registrar autostart da UI via Task Scheduler (At log on of any user)
         Call RegisterUIStartupTask

         !insertmacro wails.associateFiles
         !insertmacro wails.associateCustomProtocols

         !insertmacro wails.writeUninstaller
         WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${UNINST_KEY_NAME}" "InstallLocation" "$INSTDIR"
      !endif
SectionEnd

Section "uninstall"
    !insertmacro wails.setShellContext

   # Encerrar/remover service antes de limpar binÃ¡rios
   Call un.UnregisterWindowsService
   Call un.UnregisterWindowsFirewallRule

   # Garantir encerramento de qualquer instancia em modo UI/headless.
   ExecWait '"$SYSDIR\taskkill.exe" /IM "${PRODUCT_EXECUTABLE}" /F /T' $R0
   ${If} $R0 != 0
      DetailPrint "Aviso: taskkill retornou codigo $R0 (pode nao haver processo em execucao)."
   ${EndIf}

   ${If} "${LEGACY_PRODUCT_EXECUTABLE}" != "${PRODUCT_EXECUTABLE}"
      ExecWait '"$SYSDIR\taskkill.exe" /IM "${LEGACY_PRODUCT_EXECUTABLE}" /F /T' $R1
      ${If} $R1 != 0
         DetailPrint "Aviso: taskkill legado retornou codigo $R1 (pode nao haver processo legado em execucao)."
      ${EndIf}
   ${EndIf}

   # Executar decommission (DELETE remoto + limpeza local) antes de remover os binarios.
   IfFileExists "$INSTDIR\${PRODUCT_EXECUTABLE}" 0 decommission_done
      DetailPrint "Executando decommission do agente..."
      ExecWait '"$INSTDIR\${PRODUCT_EXECUTABLE}" --agent-delete-cleanup' $R3
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

# FunÃ§Ã£o para salvar as configuraÃ§Ãµes do agente
Function SaveAgentConfig
   # Build de update nÃ£o altera configuraÃ§Ã£o local existente.
   ${If} "${BUILD_UPDATE_INSTALL}" == "1"
      DetailPrint "Update build: mantendo configuraÃ§Ã£o existente sem sobrescrita"
      Return
   ${EndIf}

   # Runtime update (/UPDATE) tambÃ©m nÃ£o altera configuraÃ§Ã£o local existente.
   ${If} $UpdateMode == "1"
      DetailPrint "Update mode (/UPDATE): mantendo configuraÃ§Ã£o existente sem sobrescrita"
      Return
   ${EndIf}

   # Config compartilhada em ProgramData para suportar mÃºltiplos usuÃ¡rios.
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

   ; Garantir permissao de escrita para Administrators
   ExecWait '"$SYSDIR\icacls.exe" "$R0\Discovery" /grant "Administrators:(OI)(CI)(F)" /Q' $R9

   StrCpy $R1 "$R0\Discovery\config.json"
   StrCpy $R2 "$INSTDIR\config.json"
   DetailPrint "Mesclando configuracao em $R1"

   # nsJSON (plugin nativo NSIS): merge JSON sem PowerShell, sem arquivos temporarios.
   # Fluxo: carrega config existente (ou inicia vazio), aplica valores, serializa.
   # Fallback para $INSTDIR se ProgramData falhar (ex.: maquina sem disco C: padrao).

   # Tenta carregar config existente; ignora erro se arquivo nao existe (primeira instalacao).
   ClearErrors
   nsJSON::Set /file "$R1"
   ${If} ${Errors}
      ClearErrors
   ${EndIf}

   # Aplica valores — nsJSON cria o caminho de nos automaticamente se nao existir.
   ${If} $GenericMode != "1"
      ${If} $ServerUrl != ""
         nsJSON::Set "serverUrl" /value "$ServerUrl"
      ${EndIf}
      ${If} $ServerKey != ""
         nsJSON::Set "deployToken" /value "$ServerKey"
      ${EndIf}
   ${EndIf}

   ${If} $AutoProvisioning == "1"
      nsJSON::Set "autoProvisioning" /value "true"
   ${Else}
      nsJSON::Set "autoProvisioning" /value "false"
   ${EndIf}

   # Remove chave legada discoveryEnabled se existir
   ClearErrors
   nsJSON::Get /exists "discoveryEnabled" /end
   Pop $R8
   ${If} $R8 == "yes"
      nsJSON::Delete "discoveryEnabled" /end
   ${EndIf}

   ${If} $AllowInsecureTls == "1"
      nsJSON::Set "allowInsecureTls" /value "true"
   ${EndIf}

   # Configura p2p.enabled (preserva outras chaves existentes em p2p se houver)
   ${If} $AutoProvisioning == "1"
      nsJSON::Set "p2p" "enabled" /value "true"
   ${Else}
      nsJSON::Set "p2p" "enabled" /value "false"
   ${EndIf}

   # Serializa e grava em ProgramData; fallback para $INSTDIR em caso de erro.
   ClearErrors
   nsJSON::Serialize /format /file "$R1"
   ${If} ${Errors}
      ClearErrors
      DetailPrint "Falha ao gravar em $R1, tentando fallback $R2"
      nsJSON::Serialize /format /file "$R2"
      ${If} ${Errors}
         MessageBox MB_ICONSTOP "Falha ao gravar a configuracao do agente. Verifique permissoes em $R0\Discovery e $INSTDIR."
         Abort
      ${EndIf}
   ${EndIf}

   # Limpa installer.json legado
   Delete "$R0\Discovery\installer.json"
   Delete "$INSTDIR\installer.json"

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

   DetailPrint "Payload URL: $PayloadUrl"

   ${If} $PayloadFileName == ""
      StrCpy $PayloadFileName "discovery-stage2-installer.exe"
   ${EndIf}

   StrCpy $R6 "$TEMP\DiscoveryBootstrap"
   CreateDirectory "$R6"
   StrCpy $R7 "$R6\$PayloadFileName"

   DetailPrint "Baixando instalador completo de segunda etapa..."

   # Download HTTPS nativo via BITSAdmin com janela OCULTA (nsExec::ExecToLog).
   # BITS e um servico de transferencia assincrona do Windows que suporta HTTPS,
   # retoma downloads interrompidos e nao requer plugins NSIS adicionais.
   nsExec::ExecToLog '"$SYSDIR\cmd.exe" /c bitsadmin /transfer "DiscoveryStage2" /download /priority normal "$PayloadUrl" "$R7"'
   Pop $R0

   ${If} $R0 != 0
      MessageBox MB_ICONSTOP "Falha ao baixar instalador completo (codigo: $R0). Verifique a conectividade com o servidor."
      Abort
   ${EndIf}

   ${If} $PayloadSha256 != ""
      DetailPrint "Validando integridade SHA256 do payload..."

      # CertUtil (built-in Windows) + findstr: validacao nativa de hash, sem PowerShell,
      # sem arquivos temporarios, janela oculta via nsExec::ExecToLog.
      # CertUtil gera saida multi-linha (cabecalho + hash); findstr /i faz match
      # case-insensitive do hash esperado em qualquer linha.
      nsExec::ExecToLog '"$SYSDIR\cmd.exe" /c certutil -hashfile "$R7" SHA256 | findstr /i /c:"$PayloadSha256"'
      Pop $R0

      ${If} $R0 != 0
         MessageBox MB_ICONSTOP "Falha na validacao de integridade do payload (hash mismatch)."
         Abort
      ${EndIf}
   ${EndIf}

   DetailPrint "Executando instalador completo de segunda etapa..."
   ${If} $GenericMode == "1"
      ${If} $UpdateMode == "1"
         ExecWait '"$R7" /S /UPDATE /GENERIC /BOOTSTRAP_STAGE2 /AUTO_PROVISIONING=$AutoProvisioning /ALLOW_INSECURE_TLS=$AllowInsecureTls' $R0
      ${Else}
         ExecWait '"$R7" /S /GENERIC /BOOTSTRAP_STAGE2 /AUTO_PROVISIONING=$AutoProvisioning /ALLOW_INSECURE_TLS=$AllowInsecureTls' $R0
      ${EndIf}
   ${Else}
      ${If} $UpdateMode == "1"
         ExecWait '"$R7" /S /UPDATE /BOOTSTRAP_STAGE2 /URL="$ServerUrl" /KEY="$ServerKey" /AUTO_PROVISIONING=$AutoProvisioning /ALLOW_INSECURE_TLS=$AllowInsecureTls' $R0
      ${Else}
         ExecWait '"$R7" /S /BOOTSTRAP_STAGE2 /URL="$ServerUrl" /KEY="$ServerKey" /AUTO_PROVISIONING=$AutoProvisioning /ALLOW_INSECURE_TLS=$AllowInsecureTls' $R0
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
FunctionEnd

Function PrepareForInPlaceUpdate
   DetailPrint "Preparando atualizacao in-place (encerrando instancias em execucao)..."

   # Tentar remover startup task antiga e service antes de atualizar binarios.
   Call UnregisterUIStartupTask
   Call UnregisterWindowsService

   # Garantir que nenhuma instancia do app permaneceu em execucao.
   ExecWait '"$SYSDIR\taskkill.exe" /IM "${PRODUCT_EXECUTABLE}" /F /T' $R0
   ${If} $R0 != 0
      DetailPrint "Aviso: taskkill retornou codigo $R0 (pode nao haver processo em execucao)."
   ${EndIf}

   ${If} "${LEGACY_PRODUCT_EXECUTABLE}" != "${PRODUCT_EXECUTABLE}"
      ExecWait '"$SYSDIR\taskkill.exe" /IM "${LEGACY_PRODUCT_EXECUTABLE}" /F /T' $R1
      ${If} $R1 != 0
         DetailPrint "Aviso: taskkill legado retornou codigo $R1 (pode nao haver processo legado em execucao)."
      ${EndIf}
   ${EndIf}
FunctionEnd

Function RegisterWindowsService
   DetailPrint "Registrando Windows Service ${DISCOVERY_SERVICE_NAME}"

   # Remover versÃ£o anterior (idempotente)
   ExecWait '"$SYSDIR\sc.exe" stop "${DISCOVERY_SERVICE_NAME}"' $R0
   ExecWait '"$SYSDIR\sc.exe" delete "${DISCOVERY_SERVICE_NAME}"' $R0

   # Registrar serviÃ§o com inicializaÃ§Ã£o automÃ¡tica
   ExecWait '"$SYSDIR\sc.exe" create "${DISCOVERY_SERVICE_NAME}" binPath= "\"$INSTDIR\${PRODUCT_EXECUTABLE}\" --service" start= auto DisplayName= "Discovery Agent Service"' $R0
   ${If} $R0 != 0
      MessageBox MB_ICONSTOP "Falha ao registrar o Windows Service (${DISCOVERY_SERVICE_NAME}). Codigo: $R0"
      Abort
   ${EndIf}

   # Configurar recuperaÃ§Ã£o automÃ¡tica
   ExecWait '"$SYSDIR\sc.exe" failure "${DISCOVERY_SERVICE_NAME}" reset= 86400 actions= restart/5000/restart/5000/restart/5000' $R1
   ExecWait '"$SYSDIR\sc.exe" description "${DISCOVERY_SERVICE_NAME}" "Discovery background service (multi-user)"' $R1

   # Iniciar serviÃ§o apÃ³s instalaÃ§Ã£o
   ExecWait '"$SYSDIR\sc.exe" start "${DISCOVERY_SERVICE_NAME}"' $R1
   ${If} $R1 != 0
      DetailPrint "Aviso: service instalado, mas falhou ao iniciar automaticamente. Codigo: $R1"
   ${EndIf}
FunctionEnd

Function RegisterWindowsFirewallRule
   DetailPrint "Registrando regra de Windows Firewall para ${PRODUCT_EXECUTABLE}"

   # netsh advfirewall: nativo do Windows, sem dependencia de PowerShell ou plugins.
   # Remove regra anterior (idempotente) e cria nova.
   nsExec::ExecToLog '"$SYSDIR\netsh.exe" advfirewall firewall delete rule name="${DISCOVERY_FIREWALL_RULE_NAME}"'
   Pop $R0

   nsExec::ExecToLog '"$SYSDIR\netsh.exe" advfirewall firewall add rule name="${DISCOVERY_FIREWALL_RULE_NAME}" dir=in action=allow program="$INSTDIR\${PRODUCT_EXECUTABLE}" enable=yes profile=any'
   Pop $R0

   ${If} $R0 != 0
      DetailPrint "Aviso: falha ao registrar regra de Windows Firewall para ${PRODUCT_EXECUTABLE}. Codigo: $R0"
   ${EndIf}
FunctionEnd

Function RegisterUIStartupTask
   DetailPrint "Registrando tarefa de autostart da UI (${DISCOVERY_UI_TASK_NAME})"

   ; Limpar atalho legado de startup (migracao)
   Delete "$SMSTARTUP\${INFO_PRODUCTNAME}.lnk"

   ; PowerShell -Command inline: registra scheduled task sem arquivo .ps1 temporario.
   ; Aspas simples internas duplicadas (''...'') para sobreviver ao parser NSIS e cmd.
   nsExec::ExecToLog '"$SYSDIR\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -ExecutionPolicy Bypass -Command "$act=New-ScheduledTaskAction -Execute ''$INSTDIR\${PRODUCT_EXECUTABLE}'' -Argument ''--startup-minimized --startup-source=task-scheduler'';$trig=New-ScheduledTaskTrigger -AtLogOn;$trig.Delay=''PT30S'';$prin=New-ScheduledTaskPrincipal -GroupId ''S-1-5-32-545'' -RunLevel Limited;Register-ScheduledTask -TaskName ''${DISCOVERY_UI_TASK_NAME}'' -Action $act -Trigger $trig -Principal $prin -Description ''Discovery UI autostart for any logged-on user'' -Force"'
   Pop $R0

   ${If} $R0 != 0
      MessageBox MB_ICONSTOP "Falha ao registrar tarefa de autostart da UI (${DISCOVERY_UI_TASK_NAME}). Codigo: $R0"
      Abort
   ${EndIf}
FunctionEnd

Function UnregisterWindowsService
   DetailPrint "Removendo Windows Service ${DISCOVERY_SERVICE_NAME}"
   ExecWait '"$SYSDIR\sc.exe" stop "${DISCOVERY_SERVICE_NAME}"' $R0
   ${If} $R0 != 0
      DetailPrint "Aviso: falha (ou service inexistente) ao parar ${DISCOVERY_SERVICE_NAME}. Codigo: $R0"
   ${EndIf}
   ExecWait '"$SYSDIR\sc.exe" delete "${DISCOVERY_SERVICE_NAME}"' $R0
   ${If} $R0 != 0
      DetailPrint "Aviso: falha (ou service inexistente) ao remover ${DISCOVERY_SERVICE_NAME}. Codigo: $R0"
   ${EndIf}
FunctionEnd

Function un.UnregisterWindowsFirewallRule
   DetailPrint "Removendo regra de Windows Firewall de ${PRODUCT_EXECUTABLE}"

   # netsh advfirewall: nativo do Windows, sem PowerShell.
   nsExec::ExecToLog '"$SYSDIR\netsh.exe" advfirewall firewall delete rule name="${DISCOVERY_FIREWALL_RULE_NAME}"'
   Pop $R0

   ${If} $R0 != 0
      DetailPrint "Aviso: falha ao remover regra de Windows Firewall de ${PRODUCT_EXECUTABLE}. Codigo: $R0"
   ${EndIf}
FunctionEnd

Function UnregisterUIStartupTask
   DetailPrint "Removendo tarefa de autostart da UI (${DISCOVERY_UI_TASK_NAME})"
   ExecWait '"$SYSDIR\schtasks.exe" /Delete /TN "${DISCOVERY_UI_TASK_NAME}" /F' $R0
   Delete "$SMSTARTUP\${INFO_PRODUCTNAME}.lnk"
FunctionEnd

Function un.UnregisterWindowsService
   DetailPrint "Removendo Windows Service ${DISCOVERY_SERVICE_NAME}"
   ExecWait '"$SYSDIR\sc.exe" stop "${DISCOVERY_SERVICE_NAME}"' $R0
   ${If} $R0 != 0
      DetailPrint "Aviso: falha (ou service inexistente) ao parar ${DISCOVERY_SERVICE_NAME}. Codigo: $R0"
   ${EndIf}
   ExecWait '"$SYSDIR\sc.exe" delete "${DISCOVERY_SERVICE_NAME}"' $R0
   ${If} $R0 != 0
      DetailPrint "Aviso: falha (ou service inexistente) ao remover ${DISCOVERY_SERVICE_NAME}. Codigo: $R0"
   ${EndIf}
FunctionEnd

Function un.UnregisterUIStartupTask
   DetailPrint "Removendo tarefa de autostart da UI (${DISCOVERY_UI_TASK_NAME})"
   ExecWait '"$SYSDIR\schtasks.exe" /Delete /TN "${DISCOVERY_UI_TASK_NAME}" /F' $R0
   Delete "$SMSTARTUP\${INFO_PRODUCTNAME}.lnk"
FunctionEnd


