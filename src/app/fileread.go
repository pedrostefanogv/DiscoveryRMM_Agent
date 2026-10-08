package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"discovery/app/core/consent"
	applocale "discovery/app/services/locale"
)

// Limites da leitura de arquivo solicitada pela IA. O conteudo e sempre de
// texto: binario e recusado (nao faz sentido no chat e evita despejar lixo).
const (
	readFileDefaultMaxBytes = 10 * 1024
	readFileHardMaxBytes    = 64 * 1024
	readFileBinaryProbe     = 8 * 1024
)

// sensitiveReadFileHints sao caminhos que costumam conter segredos. Nao bloqueiam
// a leitura (a decisao e do usuario), mas aparecem em destaque no pedido de
// autorizacao para consentimento informado.
var sensitiveReadFileHints = []string{
	".ssh", "id_rsa", "id_ed25519", ".aws", ".azure", ".kube", ".git-credentials",
	"login data", "cookies", "unattend.xml", "ntds.dit", ".env",
	"credentials", "secrets", "postgresql.conf",
}

// ReadFileWithConsent implementa AppBridge: leitura de arquivo pedida pela IA,
// com AUTORIZACAO OBRIGATORIA do usuario em CADA leitura (nao existe "permitir
// sempre", igual a captura de tela). Sem aprovacao explicita o arquivo NAO e lido.
func (a *App) ReadFileWithConsent(ctx context.Context, path string, maxBytes int, reason string) (json.RawMessage, error) {
	cleanPath, err := validateReadFilePath(path)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = a.ctx
	}

	info, err := os.Stat(cleanPath)
	if err != nil {
		a.logReadFile("erro", cleanPath, "stat: "+err.Error())
		return nil, fmt.Errorf("arquivo inacessivel: %w", err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("o caminho e um diretorio, nao um arquivo")
	}

	if maxBytes <= 0 {
		maxBytes = readFileDefaultMaxBytes
	}
	if maxBytes > readFileHardMaxBytes {
		maxBytes = readFileHardMaxBytes
	}

	// Rótulos localizados (mesmo vocabulário do consentimento de ações em
	// core/consent). A comparação continua sendo por igualdade EXATA: qualquer
	// outra resposta (inclusive texto livre) conta como negativa — fail-closed.
	locale := applocale.DetectPreferredLocale()
	approveLabel := consent.ApproveLabel(locale)

	question := buildReadFileQuestion(cleanPath, info.Size(), reason)
	optionsJSON, _ := json.Marshal([]string{approveLabel, consent.DenyLabel(locale)})
	answer, askErr := a.AskUserContext(ctx, question, string(optionsJSON), "false")
	if askErr != nil {
		a.logReadFile("cancelado", cleanPath, askErr.Error())
		return nil, askErr
	}

	if !strings.EqualFold(strings.TrimSpace(answer), approveLabel) {
		a.logReadFile("negado", cleanPath, "resposta do usuario: "+strings.TrimSpace(answer))
		payload, _ := json.Marshal(map[string]any{
			"approved": false,
			"path":     cleanPath,
			"message":  "Leitura NAO autorizada pelo usuario. Nao insista: explique o motivo e peca de novo somente se for realmente necessario.",
		})
		return payload, nil
	}

	content, truncated, readErr := readFileCapped(cleanPath, maxBytes)
	if readErr != nil {
		a.logReadFile("erro", cleanPath, "leitura: "+readErr.Error())
		return nil, readErr
	}
	if looksBinary(content) {
		a.logReadFile("recusado-binario", cleanPath, "conteudo binario")
		payload, _ := json.Marshal(map[string]any{
			"approved": true,
			"path":     cleanPath,
			"binary":   true,
			"message":  "O arquivo foi autorizado, mas o conteudo e binario e nao pode ser exibido como texto.",
		})
		return payload, nil
	}

	a.logReadFile("autorizado", cleanPath, fmt.Sprintf("%d bytes (truncado=%v)", len(content), truncated))
	payload, _ := json.Marshal(map[string]any{
		"approved":  true,
		"path":      cleanPath,
		"bytes":     len(content),
		"truncated": truncated,
		"content":   string(content),
	})
	return payload, nil
}

// validateReadFilePath normaliza e restringe o caminho lido pela IA:
// absoluto, local (sem UNC/dispositivo) e sem caracteres de controle.
func validateReadFilePath(path string) (string, error) {
	raw := strings.TrimSpace(path)
	if raw == "" {
		return "", fmt.Errorf("path nao pode ser vazio")
	}
	if strings.ContainsAny(raw, "\x00\n\r\t") {
		return "", fmt.Errorf("path invalido")
	}
	// UNC (\\\\servidor\\share) e dispositivo (\\\\.\\x): fora do escopo de leitura
	// de um arquivo local.
	if strings.HasPrefix(raw, "\\\\") {
		return "", fmt.Errorf("caminhos de rede (UNC) ou de dispositivo nao sao permitidos")
	}
	if !filepath.IsAbs(raw) {
		return "", fmt.Errorf("informe o caminho ABSOLUTO do arquivo (ex: C:\\Users\\usuario\\arquivo.txt)")
	}
	return filepath.Clean(raw), nil
}

// buildReadFileQuestion monta o pedido de autorizacao com os dados que o usuario
// precisa para decidir (caminho, tamanho e motivo).
func buildReadFileQuestion(path string, size int64, reason string) string {
	var b strings.Builder
	b.WriteString("A IA pediu para LER este arquivo do seu computador:\n")
	b.WriteString("- Arquivo: " + path + "\n")
	b.WriteString(fmt.Sprintf("- Tamanho: %d bytes\n", size))
	if r := strings.TrimSpace(reason); r != "" {
		b.WriteString("- Motivo informado: " + r + "\n")
	}
	if hint := sensitiveReadFileHint(path); hint != "" {
		b.WriteString("\nATENCAO: o caminho contem \"" + hint + "\", que costuma guardar senhas ou tokens.\n")
	}
	b.WriteString("\nSomente a leitura deste arquivo sera feita, agora. Autorizar?")
	return b.String()
}

// sensitiveReadFileHint retorna o primeiro padrao sensivel encontrado no caminho.
func sensitiveReadFileHint(path string) string {
	lower := strings.ToLower(path)
	for _, hint := range sensitiveReadFileHints {
		if strings.Contains(lower, hint) {
			return hint
		}
	}
	return ""
}

// readFileCapped le no maximo maxBytes+1 bytes para detectar truncamento.
func readFileCapped(path string, maxBytes int) ([]byte, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, fmt.Errorf("nao foi possivel abrir o arquivo: %w", err)
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, int64(maxBytes)+1))
	if err != nil {
		return nil, false, fmt.Errorf("falha ao ler o arquivo: %w", err)
	}
	if len(data) > maxBytes {
		return data[:maxBytes], true, nil
	}
	return data, false, nil
}

// looksBinary detecta conteudo binario pela presenca de NUL nos primeiros bytes.
func looksBinary(data []byte) bool {
	probe := data
	if len(probe) > readFileBinaryProbe {
		probe = probe[:readFileBinaryProbe]
	}
	for _, b := range probe {
		if b == 0 {
			return true
		}
	}
	return false
}

// logReadFile registra a decisao de leitura no log do agente (auditoria).
func (a *App) logReadFile(outcome, path, detail string) {
	if a == nil {
		return
	}
	a.Logs.Append(fmt.Sprintf("[fileread] %s path=%q %s", outcome, path, detail))
}
