package e2e_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestF11_T1_NSISProgramDataNoUsersModify(t *testing.T) {
	nsi := readSrcFile(t, filepath.Join("build", "windows", "installer", "project.nsi"))

	// Nenhum icacls sobre "$R0\Discovery" pode conceder Modify/Full/Write para BUILTIN\Users (*S-1-5-32-545)
	// de forma recursiva sobre o diretorio raiz de configuracao (%ProgramData%\Discovery).
	reUsersOnRoot := regexp.MustCompile(`(?i)icacls\.exe[^\r\n]*\$R0\\Discovery["'][^\r\n]*\*S-1-5-32-545:\([^)]*[MFW][^)]*\)`)
	if matches := reUsersOnRoot.FindAllString(nsi, -1); len(matches) > 0 {
		t.Fatalf("F11 violacao: project.nsi concede permissao de escrita/modificacao a BUILTIN\\Users (*S-1-5-32-545) sobre %%ProgramData%%\\Discovery:\n%s",
			strings.Join(matches, "\n"))
	}

	// Deve conceder apenas SYSTEM (*S-1-5-18) e Administrators (*S-1-5-32-544) na raiz de config
	if !strings.Contains(nsi, `*S-1-5-18:(OI)(CI)(F)`) || !strings.Contains(nsi, `*S-1-5-32-544:(OI)(CI)(F)`) {
		t.Fatalf("F11 violacao: project.nsi deve conceder SYSTEM:(OI)(CI)(F) e Administrators:(OI)(CI)(F) em %%ProgramData%%\\Discovery")
	}

	// Subdiretorio WebView2 deve existir isolado para o processo da UI nao-elevada
	if !strings.Contains(nsi, `\Discovery\WebView2`) {
		t.Fatalf("F11 violacao: project.nsi deve isolar o diretorio gravavel da WebView2 em %%ProgramData%%\\Discovery\\WebView2")
	}
}

func TestF11_T1_NSISRestoresSetOutPathAfterWebView2(t *testing.T) {
	wailsTools := readSrcFile(t, filepath.Join("build", "windows", "installer", "wails_tools.nsh"))
	if !strings.Contains(wailsTools, `SetOutPath "$pluginsdir\webview2bootstrapper"`) {
		t.Fatalf("precondicao inesperada: wails_tools.nsh nao contem SetOutPath \"$pluginsdir\\webview2bootstrapper\"")
	}

	nsi := readSrcFile(t, filepath.Join("build", "windows", "installer", "project.nsi"))
	idxWebView := strings.Index(nsi, "!insertmacro wails.webview2runtime")
	idxFiles := strings.Index(nsi, "!insertmacro wails.files")
	idxService := strings.Index(nsi, `File "/oname=${SERVICE_EXECUTABLE}"`)
	if idxWebView < 0 || idxFiles < 0 || idxService < 0 {
		t.Fatalf("macros obrigatorias ausentes em project.nsi (webview=%d files=%d service=%d)", idxWebView, idxFiles, idxService)
	}
	if !(idxWebView < idxFiles && idxFiles < idxService) {
		t.Fatalf("ordem inesperada das macros em project.nsi: webview=%d files=%d service=%d", idxWebView, idxFiles, idxService)
	}

	between := nsi[idxWebView:idxFiles]
	reSetOutInstDir := regexp.MustCompile(`(?im)^\s*SetOutPath\s+"?\$INSTDIR"?\s*$`)
	if !reSetOutInstDir.MatchString(between) {
		t.Fatalf("F11 violacao: project.nsi nao restaura `SetOutPath $INSTDIR` entre `!insertmacro wails.webview2runtime` e `!insertmacro wails.files`")
	}
}

func TestF11_T1_NSISServiceBinaryMandatoryAndValidated(t *testing.T) {
	nsi := readSrcFile(t, filepath.Join("build", "windows", "installer", "project.nsi"))

	reSvcDef := regexp.MustCompile(`(?m)^!define\s+SERVICE_EXECUTABLE\s+"discovery-service\.exe"`)
	if !reSvcDef.MatchString(nsi) {
		t.Fatalf("project.nsi deve definir SERVICE_EXECUTABLE \"discovery-service.exe\"")
	}
	if !strings.Contains(nsi, "ALLOW_UI_ONLY_BUILD") || !strings.Contains(nsi, "!error") {
		t.Fatalf("project.nsi deve exigir ARG_SERVICE_AMD64_BINARY/ARM64 ou ALLOW_UI_ONLY_BUILD com !error")
	}
	if !strings.Contains(nsi, `IfFileExists "$INSTDIR\${PRODUCT_EXECUTABLE}"`) ||
		!strings.Contains(nsi, `IfFileExists "$INSTDIR\${SERVICE_EXECUTABLE}"`) {
		t.Fatalf("project.nsi deve validar a existencia pos-copia de ${PRODUCT_EXECUTABLE} e ${SERVICE_EXECUTABLE} em $INSTDIR")
	}
}

func TestF11_T1_DebugConfigWatcherRejectsUntrustedACLOrOwner(t *testing.T) {
	debugDir := filepath.Join(srcRoot(t), "app", "debug")
	fset := token.NewFileSet()
	var foundACLVerification bool

	err := filepath.WalkDir(debugDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		srcBytes, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		src := string(srcBytes)
		f, perr := parser.ParseFile(fset, path, srcBytes, 0)
		if perr != nil {
			return perr
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			name := fn.Name.Name
			if name == "StartWatcher" || name == "pollExternalConfigChangeLocked" || name == "LoadPersistedConfig" {
				start := fset.Position(fn.Pos()).Offset
				end := fset.Position(fn.End()).Offset
				if start >= 0 && end <= len(src) {
					body := src[start:end]
					if strings.Contains(body, "ACL") || strings.Contains(body, "Owner") ||
						strings.Contains(body, "SecurityDescriptor") || strings.Contains(body, "DACL") ||
						strings.Contains(body, "Verify") || strings.Contains(body, "Trusted") {
						foundACLVerification = true
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir(%s): %v", debugDir, err)
	}
	if !foundACLVerification {
		t.Fatalf("F11 violacao: app/debug (LoadPersistedConfig/StartWatcher/pollExternalConfigChangeLocked) nao valida owner/DACL/ACL de debug_config.json antes de aplicar alteracoes externas")
	}
}

func TestF11_T1_ResumeDecommissionValidatesUninstallerPath(t *testing.T) {
	appDir := filepath.Join(srcRoot(t), "app")
	fset := token.NewFileSet()
	var foundResumeFn bool
	var callsValidate bool
	var checksProtectedLocation bool

	err := filepath.WalkDir(appDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		srcBytes, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		src := string(srcBytes)
		f, perr := parser.ParseFile(fset, path, srcBytes, 0)
		if perr != nil {
			return perr
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			if fn.Name.Name == "resumeAgentDecommissionIfPending" {
				foundResumeFn = true
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					switch fun := call.Fun.(type) {
					case *ast.Ident:
						if strings.Contains(strings.ToLower(fun.Name), "validateuninstaller") {
							callsValidate = true
						}
					case *ast.SelectorExpr:
						if strings.Contains(strings.ToLower(fun.Sel.Name), "validateuninstaller") {
							callsValidate = true
						}
					}
					return true
				})
			}
			if strings.Contains(strings.ToLower(fn.Name.Name), "validateuninstaller") {
				start := fset.Position(fn.Pos()).Offset
				end := fset.Position(fn.End()).Offset
				if start >= 0 && end <= len(src) {
					body := strings.ToLower(src[start:end])
					if strings.Contains(body, "programfiles") || strings.Contains(body, "isabs") ||
						strings.Contains(body, "acl") || strings.Contains(body, "owner") ||
						strings.Contains(body, "executable") {
						checksProtectedLocation = true
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir(%s): %v", appDir, err)
	}
	if !foundResumeFn {
		t.Fatalf("funcao resumeAgentDecommissionIfPending nao encontrada em app/")
	}
	if !callsValidate {
		t.Fatalf("F11 violacao: resumeAgentDecommissionIfPending nao chama validateUninstallerPath(req.UninstallerPath) antes de executar runAgentUninstaller")
	}
	if !checksProtectedLocation {
		t.Fatalf("F11 violacao: validateUninstallerPath deve validar caminho protegido/absoluto ou ACL antes de executar o uninstaller")
	}
}
