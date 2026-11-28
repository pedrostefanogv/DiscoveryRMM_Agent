package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"discovery/app/core/tlsutil"
	"discovery/app/netutil"

	"github.com/wailsapp/wails/v3/pkg/application"
)

type AgentFileItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	FileName    string `json:"fileName"`
	Description string `json:"description,omitempty"`
	Category    string `json:"category,omitempty"`
	ContentType string `json:"contentType"`
	SizeBytes   int64  `json:"sizeBytes"`
	Sha256      string `json:"sha256,omitempty"`
	Scope       int    `json:"scope"`
	FolderPath  string `json:"folderPath,omitempty"`
}

// UnmarshalJSON aceita o campo "scope" tanto como número (0=Global, 1=Cliente,
// 2=Site) quanto como o NOME do escopo ("Global"/"Client"/"Site").
//
// A API registra JsonStringEnumConverter globalmente (Program.cs), então o
// campo chega como string. Um decode direto em int falhava
// ("cannot unmarshal string into Go struct field AgentFileItem.scope of type
// int") e derrubava a listagem INTEIRA da aba Arquivos.
//
// A tolerância vive aqui (e não num tipo próprio) de propósito: um tipo novo
// no struct vazaria para as bindings geradas do Wails como "@typedef {any}",
// expondo um detalhe interno na API pública do frontend.
func (i *AgentFileItem) UnmarshalJSON(data []byte) error {
	type agentFileItemAlias AgentFileItem
	aux := struct {
		Scope json.RawMessage `json:"scope"`
		*agentFileItemAlias
	}{agentFileItemAlias: (*agentFileItemAlias)(i)}

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if len(aux.Scope) == 0 {
		return nil
	}
	scope, err := parseAgentFileScope(aux.Scope)
	if err != nil {
		return err
	}
	i.Scope = scope
	return nil
}

// parseAgentFileScope normaliza o escopo vindo da API para o número que o
// frontend compara.
//
// Um nome de escopo desconhecido NÃO é erro: cai para 0 (Global), igual ao
// fallback que o frontend já faz (app-files.js: scopeLabels[scope] || "Global").
// Errar aqui derrubaria a listagem INTEIRA da aba Arquivos — exatamente o modo
// de falha que este decode tolerante veio corrigir. O rótulo de um item novo
// pode sair impreciso por um release; a aba ficar vazia para toda a frota é pior.
func parseAgentFileScope(raw json.RawMessage) (int, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return 0, nil
	}
	if trimmed[0] != '"' {
		var n int
		if err := json.Unmarshal(raw, &n); err != nil {
			return 0, err
		}
		return n, nil
	}
	var name string
	if err := json.Unmarshal(raw, &name); err != nil {
		return 0, err
	}
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "global":
		return 0, nil
	case "client", "cliente":
		return 1, nil
	case "site":
		return 2, nil
	default:
		return 0, nil
	}
}

type AgentFileDownloadResult struct {
	SavedPath string `json:"savedPath"`
	FileName  string `json:"fileName"`
	SizeBytes int64  `json:"sizeBytes"`
	Sha256    string `json:"sha256"`
	Verified  bool   `json:"verified"`
}

type agentPresignedDownloadResp struct {
	DownloadUrl      string `json:"downloadUrl"`
	FileName         string `json:"fileName"`
	SizeBytes        int64  `json:"sizeBytes"`
	Sha256           string `json:"sha256"`
	ExpiresInSeconds int    `json:"expiresInSeconds"`
}

func (a *App) ListAgentFiles(ctx context.Context) ([]AgentFileItem, error) {
	if a == nil {
		return nil, fmt.Errorf("app não inicializado")
	}
	if raw, err, handled := a.filesCompanionCall(ctx, "files:list", nil, 20*time.Second); handled {
		if err != nil {
			return nil, err
		}
		var items []AgentFileItem
		if uErr := json.Unmarshal(raw, &items); uErr != nil {
			return nil, uErr
		}
		if items == nil {
			items = []AgentFileItem{}
		}
		return items, nil
	}
	return a.listAgentFilesLocal(ctx)
}

func (a *App) listAgentFilesLocal(ctx context.Context) ([]AgentFileItem, error) {
	if a == nil {
		return nil, fmt.Errorf("app não inicializado")
	}
	cfg := a.getDebugConfigInternal()
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.ApiServer), "/")
	token := strings.TrimSpace(cfg.AuthToken)
	agentID := strings.TrimSpace(cfg.AgentID)
	if baseURL == "" || token == "" || agentID == "" {
		return nil, fmt.Errorf("agente sem credenciais de servidor configuradas")
	}

	if ctx == nil {
		ctx = context.Background()
	}
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, baseURL+"/api/v1/agent-auth/me/files", nil)
	if err != nil {
		return nil, err
	}
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, token, agentID); err != nil {
		return nil, err
	}

	resp, err := tlsutil.NewHTTPClient(15 * time.Second).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("API retornou HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var items []AgentFileItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, err
	}
	if items == nil {
		items = []AgentFileItem{}
	}
	return items, nil
}

func (a *App) DownloadAgentFile(ctx context.Context, fileID, fileName string, chooseLocation bool) (*AgentFileDownloadResult, error) {
	if a == nil {
		return nil, fmt.Errorf("app não inicializado")
	}
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return nil, fmt.Errorf("id do arquivo é obrigatório")
	}

	var meta agentPresignedDownloadResp
	if raw, err, handled := a.filesCompanionCall(ctx, "files:download_url", map[string]any{"fileId": fileID}, 25*time.Second); handled {
		if err != nil {
			return nil, err
		}
		if uErr := json.Unmarshal(raw, &meta); uErr != nil {
			return nil, uErr
		}
	} else {
		var lErr error
		meta, lErr = a.requestAgentFileDownloadMetaLocal(ctx, fileID)
		if lErr != nil {
			return nil, lErr
		}
	}

	safeName := sanitizeAttachmentFileName(firstNonEmptyString(meta.FileName, fileName, "arquivo"))
	var targetPath string
	var err error
	if chooseLocation && a.app != nil {
		dialog := a.app.Dialog().SaveFile()
		dialog.SetOptions(&uiSaveFileDialogOptions{
			Title:                "Salvar arquivo",
			Filename:             safeName,
			Directory:            existingAttachmentDir(),
			CanCreateDirectories: true,
			AllowOtherFileTypes:  true,
		})
		chosen, err := dialog.PromptForSingleSelection()
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(chosen) == "" {
			return nil, fmt.Errorf("download cancelado pelo usuário")
		}
		targetPath, err = resolveAttachmentExactTarget(chosen, safeName)
		if err != nil {
			return nil, err
		}
	} else {
		targetPath, err = resolveAttachmentDirTarget(resolveAttachmentDownloadDir(), safeName)
		if err != nil {
			return nil, err
		}
	}

	return a.streamDownloadAndVerify(ctx, fileID, meta.DownloadUrl, targetPath, safeName, meta.SizeBytes, meta.Sha256)
}

func (a *App) streamDownloadAndVerify(
	ctx context.Context,
	fileID string,
	downloadURL string,
	targetPath string,
	fileName string,
	expectedSize int64,
	expectedSha256 string,
) (*AgentFileDownloadResult, error) {
	dlReq, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, err
	}

	// Cliente com TLS flexível se configurado no agent, timeout longo para arquivos grandes.
	dlResp, err := tlsutil.NewHTTPClient(30 * time.Minute).Do(dlReq)
	if err != nil {
		return nil, err
	}
	defer dlResp.Body.Close()

	if dlResp.StatusCode < 200 || dlResp.StatusCode >= 300 {
		return nil, fmt.Errorf("storage retornou HTTP %d durante o download", dlResp.StatusCode)
	}

	out, err := os.CreateTemp(filepath.Dir(targetPath), ".discovery-dl-*.part")
	if err != nil {
		return nil, err
	}
	tmpPath := out.Name()

	cleanupTmp := true
	defer func() {
		if out != nil {
			_ = out.Close()
		}
		if cleanupTmp {
			_ = os.Remove(tmpPath)
		}
	}()

	hasher := sha256.New()
	totalBytes := expectedSize
	if dlResp.ContentLength > 0 {
		totalBytes = dlResp.ContentLength
	}

	buf := make([]byte, 64*1024)
	var written int64
	lastEmit := time.Time{}

	for {
		n, readErr := dlResp.Body.Read(buf)
		if n > 0 {
			if _, wErr := out.Write(buf[:n]); wErr != nil {
				return nil, wErr
			}
			_, _ = hasher.Write(buf[:n])
			written += int64(n)

			if a != nil && a.app != nil && time.Since(lastEmit) >= 120*time.Millisecond {
				lastEmit = time.Now()
				pct := 0
				if totalBytes > 0 {
					pct = int((written * 100) / totalBytes)
					if pct > 100 {
						pct = 100
					}
				}
				a.app.EmitEvent("files:download-progress", map[string]any{
					"fileId":     fileID,
					"written":    written,
					"total":      totalBytes,
					"percentage": pct,
				})
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}

	if err := out.Close(); err != nil {
		out = nil
		return nil, err
	}
	out = nil

	actualSha256 := hex.EncodeToString(hasher.Sum(nil))
	verified := false
	if exp := strings.ToLower(strings.TrimSpace(expectedSha256)); exp != "" {
		if actualSha256 != exp {
			return nil, fmt.Errorf("falha na verificação de integridade SHA-256 (esperado %s, obtido %s)", exp, actualSha256)
		}
		verified = true
	}

	if err := os.Rename(tmpPath, targetPath); err != nil {
		return nil, err
	}
	cleanupTmp = false

	if a != nil && a.app != nil {
		a.app.EmitEvent("files:download-progress", map[string]any{
			"fileId":     fileID,
			"written":    written,
			"total":      written,
			"percentage": 100,
			"done":       true,
			"savedPath":  targetPath,
		})
	}

	return &AgentFileDownloadResult{
		SavedPath: targetPath,
		FileName:  fileName,
		SizeBytes: written,
		Sha256:    actualSha256,
		Verified:  verified,
	}, nil
}

func (a *App) OpenDownloadedFileFolder(filePath string) error {
	clean := filepath.Clean(strings.TrimSpace(filePath))
	if clean == "" || clean == "." {
		return fmt.Errorf("caminho inválido")
	}
	if _, err := os.Stat(clean); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return exec.Command("explorer.exe", "/select,"+clean).Start()
	}
	return application.Get().Browser.OpenURL("file://" + filepath.Dir(clean))
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func (a *App) requestAgentFileDownloadMetaLocal(ctx context.Context, fileID string) (agentPresignedDownloadResp, error) {
	var zero agentPresignedDownloadResp
	if a == nil {
		return zero, fmt.Errorf("app não inicializado")
	}
	cfg := a.getDebugConfigInternal()
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.ApiServer), "/")
	token := strings.TrimSpace(cfg.AuthToken)
	agentID := strings.TrimSpace(cfg.AgentID)
	if baseURL == "" || token == "" || agentID == "" {
		return zero, fmt.Errorf("agente sem credenciais de servidor configuradas")
	}

	if ctx == nil {
		ctx = context.Background()
	}
	reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, baseURL+"/api/v1/agent-auth/me/files/"+fileID+"/download-url", nil)
	if err != nil {
		return zero, err
	}
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, token, agentID); err != nil {
		return zero, err
	}

	resp, err := tlsutil.NewHTTPClient(20 * time.Second).Do(req)
	if err != nil {
		return zero, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return zero, fmt.Errorf("erro ao solicitar download (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var meta agentPresignedDownloadResp
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return zero, err
	}
	return meta, nil
}

func (a *App) filesCompanionCall(ctx context.Context, method string, payload map[string]any, timeout time.Duration) (json.RawMessage, error, bool) {
	if a == nil || a.ipcClient == nil {
		return nil, nil, false
	}
	base := ctx
	if base == nil {
		base = a.ctx
	}
	if base == nil {
		base = context.Background()
	}
	reqCtx, cancel := context.WithTimeout(base, timeout)
	defer cancel()

	resp, err := a.ipcClient.Request(reqCtx, method, payload)
	if err != nil {
		if isUnknownIPCMethod(err) {
			return nil, nil, false
		}
		return nil, err, true
	}
	raw, dErr := decodeIPCSupportResult(method, resp)
	if dErr != nil {
		return nil, dErr, true
	}
	return raw, nil, true
}

func (a *App) ipcRPCFilesList(ctx context.Context) map[string]any {
	items, err := a.listAgentFilesLocal(ctx)
	if err != nil {
		return ipcRPCError(err)
	}
	return ipcRPCResult(items)
}

func (a *App) ipcRPCFilesDownloadURL(ctx context.Context, payload map[string]any) map[string]any {
	fileID := ipcRPCString(payload, "fileId")
	if fileID == "" {
		return ipcRPCError(fmt.Errorf("id do arquivo é obrigatório"))
	}
	meta, err := a.requestAgentFileDownloadMetaLocal(ctx, fileID)
	if err != nil {
		return ipcRPCError(err)
	}
	return ipcRPCResult(meta)
}
