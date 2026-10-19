package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	appsupport "discovery/app/support"
)

// ── Anexos do chamado: listar e SALVAR EM DISCO ───────────────────────────
//
// O webview recebia o arquivo em base64 e montava um data URL em JS. Isso até
// serve para o nível conversa (10 MB), mas incha a memória do renderer e é
// inviável para o repositório do chamado (até 10 GB, com multipart). Aqui os
// bytes são gravados direto em disco e a UI recebe só o caminho final.
//
// A pasta de destino é resolvida do lado do USUÁRIO (não do processo que grava):
// em modo companion quem grava é o serviço, que roda como SYSTEM — e
// os.UserHomeDir() ali aponta para C:\WINDOWS\system32\config\systemprofile.
// Sem isso o anexo era salvo em
// C:\WINDOWS\system32\config\systemprofile\Downloads, invisível para quem
// clicou em baixar.

// attachmentDownloadDirOverrideEnv permite fixar a pasta padrão de download sem
// recompilar (instalações com perfil redirecionado/suporte).
const attachmentDownloadDirOverrideEnv = "DISCOVERY_DOWNLOAD_DIR"

// sanitizeAttachmentFileName remove caminho/caracteres perigosos do nome
// sugerido pelo servidor (o nome vem de quem fez o upload).
func sanitizeAttachmentFileName(name string) string {
	clean := strings.TrimSpace(filepath.Base(strings.TrimSpace(name)))
	replacer := strings.NewReplacer(
		"\\", "-", "/", "-", ":", "-", "*", "-", "?", "-",
		"\"", "-", "<", "-", ">", "-", "|", "-",
	)
	clean = strings.TrimSpace(replacer.Replace(clean))
	if clean == "" || clean == "." {
		clean = "anexo"
	}
	return clean
}

// isSystemProfilePath identifica o perfil do sistema (processo rodando como
// SYSTEM/sessão 0). Nesse contexto as pastas pessoais apontam para o perfil do
// serviço, que o usuário não enxerga.
func isSystemProfilePath(dir string) bool {
	normalized := strings.ToLower(filepath.ToSlash(strings.TrimSpace(dir)))
	return strings.Contains(normalized, "systemprofile")
}

// resolveAttachmentDownloadDir devolve a pasta padrão onde os anexos baixados
// são gravados.
//
// Ordem: override explícito > Downloads do usuário ATUAL (quando o processo não
// roda no perfil do sistema) > Downloads do usuário da sessão interativa
// (serviço/SYSTEM) > Downloads Público > pasta temporária.
func resolveAttachmentDownloadDir() string {
	if override := strings.TrimSpace(os.Getenv(attachmentDownloadDirOverrideEnv)); override != "" {
		return override
	}
	if home, err := os.UserHomeDir(); err == nil {
		home = strings.TrimSpace(home)
		if home != "" && !isSystemProfilePath(home) {
			return filepath.Join(home, "Downloads")
		}
	}
	if dir := strings.TrimSpace(activeUserDownloadsDir()); dir != "" {
		return dir
	}
	if public := strings.TrimSpace(os.Getenv("PUBLIC")); public != "" {
		return filepath.Join(public, "Downloads")
	}
	return os.TempDir()
}

// existingAttachmentDir devolve a pasta padrão apenas quando ela já existe: o
// diálogo nativo ignora um diretório inicial inexistente.
func existingAttachmentDir() string {
	dir := strings.TrimSpace(resolveAttachmentDownloadDir())
	if dir == "" {
		return ""
	}
	if info, err := os.Stat(dir); err == nil && info.IsDir() {
		return dir
	}
	return ""
}

// saveDownloadedAttachmentToDir grava os bytes em dir (criando-a se preciso) e
// devolve o caminho absoluto. Nunca sobrescreve: acrescenta timestamp.
func saveDownloadedAttachmentToDir(dir, fileName string, data []byte) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("anexo vazio")
	}
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "", fmt.Errorf("pasta de destino vazia")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("não foi possível criar a pasta de destino: %w", err)
	}

	target := filepath.Join(dir, sanitizeAttachmentFileName(fileName))
	if _, err := os.Stat(target); err == nil {
		ext := filepath.Ext(target)
		base := strings.TrimSuffix(target, ext)
		target = fmt.Sprintf("%s-%s%s", base, time.Now().Format("20060102-150405"), ext)
	}
	if err := os.WriteFile(target, data, 0o644); err != nil {
		return "", fmt.Errorf("não foi possível salvar o anexo: %w", err)
	}
	return target, nil
}

// saveDownloadedAttachment grava os bytes na pasta padrão de download e devolve
// o caminho absoluto.
func saveDownloadedAttachment(fileName string, data []byte) (string, error) {
	return saveDownloadedAttachmentToDir(resolveAttachmentDownloadDir(), fileName, data)
}

// saveAttachmentBytesAt grava os bytes no caminho EXATO escolhido pelo usuário
// no diálogo "Salvar como". Não acrescenta timestamp: o diálogo nativo já pediu
// confirmação de sobrescrita. Se o destino for uma PASTA, cai no fluxo por pasta.
func saveAttachmentBytesAt(destPath, fallbackName string, data []byte) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("anexo vazio")
	}
	clean := filepath.Clean(strings.TrimSpace(destPath))
	if clean == "" || clean == "." || !filepath.IsAbs(clean) {
		return "", fmt.Errorf("caminho de destino inválido: %q", destPath)
	}
	if info, err := os.Stat(clean); err == nil && info.IsDir() {
		return saveDownloadedAttachmentToDir(clean, fallbackName, data)
	}
	if err := os.MkdirAll(filepath.Dir(clean), 0o755); err != nil {
		return "", fmt.Errorf("não foi possível criar a pasta de destino: %w", err)
	}
	if err := os.WriteFile(clean, data, 0o644); err != nil {
		return "", fmt.Errorf("não foi possível salvar o anexo: %w", err)
	}
	return clean, nil
}

// GetTicketAttachments lista os anexos do chamado (repositório + conversa).
func (a *App) GetTicketAttachments(ticketID string) ([]appsupport.TicketAttachment, error) {
	if err := a.requireSupportSvc(); err != nil {
		return nil, err
	}
	return supportReadOrLocal(a, "support:ticket_attachments_all", map[string]any{"ticketId": ticketID},
		func() ([]appsupport.TicketAttachment, error) {
			return a.SupportSvc.GetTicketAttachments(ticketID)
		})
}

// ChooseTicketAttachmentSavePath abre o diálogo NATIVO "Salvar como" no processo
// da UI (usuário interativo) e devolve o caminho completo escolhido. Devolve
// string vazia quando o usuário cancela. O download em si continua no serviço
// (dono do token e do SQLite) — a UI só escolhe ONDE gravar.
//
// Sem webview (serviço/standalone) não há diálogo: devolve o caminho padrão.
func (a *App) ChooseTicketAttachmentSavePath(fileName, title string) (string, error) {
	name := sanitizeAttachmentFileName(fileName)
	if a == nil || a.app == nil {
		return filepath.Join(resolveAttachmentDownloadDir(), name), nil
	}
	if strings.TrimSpace(title) == "" {
		title = "Salvar anexo"
	}
	// SaveFileDialogStruct não expõe SetTitle no Wails v3 beta e o campo
	// "message" é ignorado no diálogo nativo do Windows: o título real vem de
	// SetOptions.
	dlg := a.app.Dialog.SaveFile()
	dlg.SetOptions(&application.SaveFileDialogOptions{
		Title:                title,
		Filename:             name,
		Directory:            existingAttachmentDir(),
		CanCreateDirectories: true,
		AllowOtherFileTypes:  true,
	})
	return dlg.PromptForSingleSelection()
}

// saveAttachmentRPC monta o payload comum do RPC: destDir (pasta padrão
// resolvida na UI) ou destPath (escolhido no "Salvar como").
func attachmentSavePayload(fields map[string]any, destDir, destPath string) map[string]any {
	payload := map[string]any{}
	for key, value := range fields {
		payload[key] = value
	}
	if strings.TrimSpace(destPath) != "" {
		payload["destPath"] = destPath
		return payload
	}
	if strings.TrimSpace(destDir) != "" {
		payload["destDir"] = destDir
	}
	return payload
}

// attachmentSaveResult é o resultado do RPC de gravação. O campo PRECISA ser
// exportado: em modo companion o valor é reidratado com json.Unmarshal, que
// ignora campos não exportados e devolveria caminho vazio para a UI.
type attachmentSaveResult struct {
	Path string
}

// SaveTicketAttachment baixa um anexo do REPOSITÓRIO e salva na pasta padrão
// (Downloads do usuário). A pasta é resolvida no processo da UI e vai no RPC,
// para não cair no perfil do SYSTEM.
func (a *App) SaveTicketAttachment(ticketID, attachmentID, fileName string) (string, error) {
	if err := a.requireSupportSvc(); err != nil {
		return "", err
	}
	res, err := supportWriteOrLocal(a, "support:ticket_attachment_save",
		attachmentSavePayload(map[string]any{
			"ticketId":     ticketID,
			"attachmentId": attachmentID,
			"fileName":     fileName,
		}, resolveAttachmentDownloadDir(), ""), func() (attachmentSaveResult, error) {
			data, _, downloadErr := a.SupportSvc.DownloadTicketAttachment(ticketID, attachmentID)
			if downloadErr != nil {
				return attachmentSaveResult{}, downloadErr
			}
			path, saveErr := saveDownloadedAttachment(fileName, data)
			if saveErr != nil {
				return attachmentSaveResult{}, saveErr
			}
			return attachmentSaveResult{Path: path}, nil
		})
	if err != nil {
		return "", err
	}
	return res.Path, nil
}

// SaveTicketAttachmentAs salva um anexo do REPOSITÓRIO no caminho escolhido no
// diálogo nativo (Save As).
func (a *App) SaveTicketAttachmentAs(ticketID, attachmentID, fileName, destPath string) (string, error) {
	if err := a.requireSupportSvc(); err != nil {
		return "", err
	}
	res, err := supportWriteOrLocal(a, "support:ticket_attachment_save",
		attachmentSavePayload(map[string]any{
			"ticketId":     ticketID,
			"attachmentId": attachmentID,
			"fileName":     fileName,
		}, "", destPath), func() (attachmentSaveResult, error) {
			data, _, downloadErr := a.SupportSvc.DownloadTicketAttachment(ticketID, attachmentID)
			if downloadErr != nil {
				return attachmentSaveResult{}, downloadErr
			}
			path, saveErr := saveAttachmentBytesAt(destPath, fileName, data)
			if saveErr != nil {
				return attachmentSaveResult{}, saveErr
			}
			return attachmentSaveResult{Path: path}, nil
		})
	if err != nil {
		return "", err
	}
	return res.Path, nil
}

// SaveTicketCommentAttachment baixa um anexo da CONVERSA e salva na pasta
// padrão (Downloads do usuário).
func (a *App) SaveTicketCommentAttachment(ticketID, commentID, attachmentID, fileName string) (string, error) {
	if err := a.requireSupportSvc(); err != nil {
		return "", err
	}
	res, err := supportWriteOrLocal(a, "support:comment_attachment_save",
		attachmentSavePayload(map[string]any{
			"ticketId":     ticketID,
			"commentId":    commentID,
			"attachmentId": attachmentID,
			"fileName":     fileName,
		}, resolveAttachmentDownloadDir(), ""), func() (attachmentSaveResult, error) {
			data, _, downloadErr := a.SupportSvc.DownloadTicketCommentAttachment(ticketID, commentID, attachmentID)
			if downloadErr != nil {
				return attachmentSaveResult{}, downloadErr
			}
			path, saveErr := saveDownloadedAttachment(fileName, data)
			if saveErr != nil {
				return attachmentSaveResult{}, saveErr
			}
			return attachmentSaveResult{Path: path}, nil
		})
	if err != nil {
		return "", err
	}
	return res.Path, nil
}

// SaveTicketCommentAttachmentAs salva um anexo da CONVERSA no caminho escolhido
// no diálogo nativo (Save As).
func (a *App) SaveTicketCommentAttachmentAs(ticketID, commentID, attachmentID, fileName, destPath string) (string, error) {
	if err := a.requireSupportSvc(); err != nil {
		return "", err
	}
	res, err := supportWriteOrLocal(a, "support:comment_attachment_save",
		attachmentSavePayload(map[string]any{
			"ticketId":     ticketID,
			"commentId":    commentID,
			"attachmentId": attachmentID,
			"fileName":     fileName,
		}, "", destPath), func() (attachmentSaveResult, error) {
			data, _, downloadErr := a.SupportSvc.DownloadTicketCommentAttachment(ticketID, commentID, attachmentID)
			if downloadErr != nil {
				return attachmentSaveResult{}, downloadErr
			}
			path, saveErr := saveAttachmentBytesAt(destPath, fileName, data)
			if saveErr != nil {
				return attachmentSaveResult{}, saveErr
			}
			return attachmentSaveResult{Path: path}, nil
		})
	if err != nil {
		return "", err
	}
	return res.Path, nil
}

// ── Anexos do CHAT AO VIVO (janela dedicada) ──────────────────────────────
//
// Antes a janela do chat montava um Blob em JS e clicava em <a download>: o
// WebView2 abria o painel de downloads do Edge (um "modal" que cobre a
// conversa) e cada clique baixava o arquivo DE NOVO, inclusive quando o
// usuário só queria reabrir o que já tinha salvado.
//
// Agora o Go grava direto na pasta padrão (Downloads do usuário resolvida no
// processo da UI) e devolve o caminho. A UI guarda esse caminho por anexo e,
// num segundo clique, apenas informa onde o arquivo está — sem novo download e
// sem o painel do navegador.

// supportChatAttachmentData baixa os bytes de um anexo do chat ao vivo.
// Centraliza validação/contexto usados pelo download (visor/miniatura) e pela
// gravação em disco.
func (a *App) supportChatAttachmentData(attachmentID string) ([]byte, error) {
	attachmentID = strings.TrimSpace(attachmentID)
	if attachmentID == "" {
		return nil, errSupportChatInvalidAttachment
	}

	ctx, cancel := context.WithTimeout(a.supportChatContext(), supportChatCallWait)
	defer cancel()

	data, _, err := a.supportChatService().DownloadAttachment(ctx, attachmentID)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("anexo vazio")
	}
	return data, nil
}

// ChooseSupportChatAttachmentSavePath abre o diálogo NATIVO "Salvar como" para
// um anexo do chat. Reaproveita o mesmo diálogo dos anexos de chamado (a janela
// do chat roda no processo da UI, onde o diálogo nativo existe).
func (a *App) ChooseSupportChatAttachmentSavePath(fileName, title string) (string, error) {
	return a.ChooseTicketAttachmentSavePath(fileName, title)
}

// SupportChatSaveAttachment baixa um anexo do chat ao vivo e salva na pasta
// padrão (Downloads do usuário). Devolve o caminho absoluto gravado.
func (a *App) SupportChatSaveAttachment(attachmentID, fileName string) (string, error) {
	data, err := a.supportChatAttachmentData(attachmentID)
	if err != nil {
		return "", err
	}
	return saveDownloadedAttachment(fileName, data)
}

// SupportChatSaveAttachmentAs salva um anexo do chat no caminho escolhido no
// diálogo nativo (Save As).
func (a *App) SupportChatSaveAttachmentAs(attachmentID, fileName, destPath string) (string, error) {
	data, err := a.supportChatAttachmentData(attachmentID)
	if err != nil {
		return "", err
	}
	return saveAttachmentBytesAt(destPath, fileName, data)
}
