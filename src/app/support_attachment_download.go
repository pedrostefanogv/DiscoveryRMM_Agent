package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	appsupport "discovery/app/support"
)

// ── Anexos do chamado: listar e SALVAR EM DISCO ───────────────────────────
//
// O webview recebia o arquivo em base64 e montava um data URL em JS. Isso até
// serve para o nível conversa (10 MB), mas incha a memória do renderer e é
// inviável para o repositório do chamado (até 10 GB, com multipart). Aqui os
// bytes são gravados direto em disco e a UI recebe só o caminho final.

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

// saveDownloadedAttachment grava os bytes em <home>/Downloads (fallback: temp)
// e devolve o caminho absoluto. Nunca sobrescreve: acrescenta timestamp.
func saveDownloadedAttachment(fileName string, data []byte) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("anexo vazio")
	}

	dir := ""
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		dir = filepath.Join(home, "Downloads")
	}
	if dir == "" {
		dir = os.TempDir()
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

// SaveTicketAttachment baixa um anexo do REPOSITÓRIO e salva em disco.
func (a *App) SaveTicketAttachment(ticketID, attachmentID, fileName string) (string, error) {
	if err := a.requireSupportSvc(); err != nil {
		return "", err
	}
	type result struct {
		Path string `json:"path"`
	}
	res, err := supportWriteOrLocal(a, "support:ticket_attachment_save", map[string]any{
		"ticketId":     ticketID,
		"attachmentId": attachmentID,
		"fileName":     fileName,
	}, func() (result, error) {
		data, _, downloadErr := a.SupportSvc.DownloadTicketAttachment(ticketID, attachmentID)
		if downloadErr != nil {
			return result{}, downloadErr
		}
		path, saveErr := saveDownloadedAttachment(fileName, data)
		if saveErr != nil {
			return result{}, saveErr
		}
		return result{Path: path}, nil
	})
	if err != nil {
		return "", err
	}
	return res.Path, nil
}

// SaveTicketCommentAttachment baixa um anexo da CONVERSA e salva em disco.
func (a *App) SaveTicketCommentAttachment(ticketID, commentID, attachmentID, fileName string) (string, error) {
	if err := a.requireSupportSvc(); err != nil {
		return "", err
	}
	type result struct {
		Path string `json:"path"`
	}
	res, err := supportWriteOrLocal(a, "support:comment_attachment_save", map[string]any{
		"ticketId":     ticketID,
		"commentId":    commentID,
		"attachmentId": attachmentID,
		"fileName":     fileName,
	}, func() (result, error) {
		data, _, downloadErr := a.SupportSvc.DownloadTicketCommentAttachment(ticketID, commentID, attachmentID)
		if downloadErr != nil {
			return result{}, downloadErr
		}
		path, saveErr := saveDownloadedAttachment(fileName, data)
		if saveErr != nil {
			return result{}, saveErr
		}
		return result{Path: path}, nil
	})
	if err != nil {
		return "", err
	}
	return res.Path, nil
}
