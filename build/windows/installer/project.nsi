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
!define INFO_PROJECTNAME    "meduza-discovery"
!define INFO_COMPANYNAME    "Meduza"
!define INFO_PRODUCTNAME    "Meduza Discovery"
!define INFO_PRODUCTVERSION "1.0.0"
!define INFO_COPYRIGHT      "Copyright (c) 2026 Meduza"
!define PRODUCT_EXECUTABLE  "meduza-discovery.exe"
!define UNINST_KEY_NAME     "Meduza.Discovery"
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
OutFile "..\..\bin\${INFO_PROJECTNAME}-${ARCH}-installer.exe" # Name of the installer's file.
InstallDir "$PROGRAMFILES64\${INFO_COMPANYNAME}\${INFO_PRODUCTNAME}" # Default installing folder ($PROGRAMFILES is Program Files folder).
ShowInstDetails show # This will always show the installation details.

Function .onInit
   !insertmacro wails.checkArchitecture
   
   # Definir valores padrão
   StrCpy $ServerUrl ""
   StrCpy $ServerKey ""
   StrCpy $DiscoveryEnabled "1"
   StrCpy $MinimalMode "0"
   
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
   
   # Parse DISCOVERY (0 ou 1)
   ${GetOptions} $R0 "/DISCOVERY=" $R1
   ${If} $R1 != ""
      StrCpy $DiscoveryEnabled $R1
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
FunctionEnd

# Função para criar a página de configuração do agente
Function AgentConfigPage
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

    !insertmacro wails.webview2runtime

    SetOutPath $INSTDIR

    !insertmacro wails.files

    CreateShortcut "$SMPROGRAMS\${INFO_PRODUCTNAME}.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}"
    CreateShortCut "$DESKTOP\${INFO_PRODUCTNAME}.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}"
    
    # Salvar configurações do agente
    Call SaveAgentConfig

    !insertmacro wails.associateFiles
    !insertmacro wails.associateCustomProtocols

    !insertmacro wails.writeUninstaller
SectionEnd

Section "uninstall"
    !insertmacro wails.setShellContext

    RMDir /r "$AppData\${PRODUCT_EXECUTABLE}" # Remove the WebView2 DataPath

    RMDir /r $INSTDIR

    Delete "$SMPROGRAMS\${INFO_PRODUCTNAME}.lnk"
    Delete "$DESKTOP\${INFO_PRODUCTNAME}.lnk"

    !insertmacro wails.unassociateFiles
    !insertmacro wails.unassociateCustomProtocols

    !insertmacro wails.deleteUninstaller
SectionEnd

# Função para salvar as configurações do agente
Function SaveAgentConfig
   # Criar arquivo de configuração em $INSTDIR\config.json
   FileOpen $0 "$INSTDIR\config.json" w
   
   FileWrite $0 "{$\r$\n"
   FileWrite $0 '  "serverUrl": "$ServerUrl",$\r$\n'
   FileWrite $0 '  "apiKey": "$ServerKey",$\r$\n'
   FileWrite $0 '  "discoveryEnabled": $DiscoveryEnabled$\r$\n'
   FileWrite $0 "}$\r$\n"
   
   FileClose $0
FunctionEnd
