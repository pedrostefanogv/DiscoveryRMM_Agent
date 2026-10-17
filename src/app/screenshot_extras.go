package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"discovery/app/core/screen"
	"discovery/app/core/screenshot"
)

// ── Galeria local, evidência e OCR de capturas ────────────────────────────
//
// Bindings da galeria ("Discovery Capturas") e do modo evidência. As regras de
// nome/formato/hash e a costura de rolagem ficam em core/screenshot (puras,
// testáveis); aqui só há I/O e conversão para o contrato JSON da UI.

const (
	// screenshotGalleryDirEnv permite fixar a pasta da galeria em instalações
	// com perfil redirecionado. Vazio em produção.
	screenshotGalleryDirEnv = "DISCOVERY_SCREENSHOT_GALLERY_DIR"
	// ocrPowerShellTimeout limita a chamada ao motor do Windows: OCR travado não
	// pode deixar a UI esperando indefinidamente.
	ocrPowerShellTimeout = 20 * time.Second
	// scrollingScrollNotches é o quanto uma rodada de rolagem desce. A costura
	// depende de sobreposição, então rolar demais (mais de uma tela) quebraria a
	// detecção; 3 notches ficam bem abaixo da altura de uma tela típica.
	scrollingScrollNotches = 3
	// scrollingSettleDelay dá tempo do app repintar depois da roda do mouse.
	scrollingSettleDelay = 350 * time.Millisecond
	// scrollingMaxScrolls/scrollingMinScrolls delimitam o parâmetro da UI.
	scrollingMaxScrolls = 20
	scrollingMinScrolls = 1
)

// screenshotGalleryDirOverride isola os testes da pasta real do usuário.
var screenshotGalleryDirOverride string

// screenshotRandomSuffix gera o sufixo aleatório do nome do arquivo. É uma
// variável para o teste conseguir forçar colisão de nome (com o gerador real a
// coincidência depende do segundo e do crypto/rand, não é reproduzível).
var screenshotRandomSuffix = screenshot.RandomSuffix

// resolveScreenshotGalleryDir devolve %USERPROFILE%\Pictures\Discovery Capturas.
//
// Em modo serviço (SYSTEM) os.UserHomeDir aponta para o perfil do sistema, que o
// usuário não enxerga; nesse caso usamos PUBLIC\Pictures, que é gravável e
// visível para a sessão interativa.
func resolveScreenshotGalleryDir() (string, error) {
	if override := strings.TrimSpace(screenshotGalleryDirOverride); override != "" {
		return filepath.Abs(override)
	}
	if override := strings.TrimSpace(os.Getenv(screenshotGalleryDirEnv)); override != "" {
		return filepath.Abs(override)
	}
	home, _ := os.UserHomeDir()
	home = strings.TrimSpace(home)
	var base string
	switch {
	case home != "" && !isSystemProfilePath(home):
		base = filepath.Join(home, "Pictures")
	case strings.TrimSpace(os.Getenv("PUBLIC")) != "":
		base = filepath.Join(strings.TrimSpace(os.Getenv("PUBLIC")), "Pictures")
	case home != "":
		base = filepath.Join(home, "Pictures")
	default:
		return "", fmt.Errorf("nao foi possivel resolver a pasta Pictures do usuario")
	}
	return filepath.Abs(filepath.Join(base, screenshot.GalleryFolderName))
}

// screenshotGalleryFolder garante que a pasta exista e devolve o caminho
// absoluto (o guard de leitura/exclusão compara caminhos resolvidos).
func (a *App) screenshotGalleryFolder() (string, error) {
	dir, err := resolveScreenshotGalleryDir()
	if err != nil {
		return "", fmt.Errorf("pasta da galeria de capturas: %w", err)
	}
	dir = filepath.Clean(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("nao foi possivel criar a pasta da galeria: %w", err)
	}
	return dir, nil
}

// SaveScreenshotImage grava a imagem em disco (PNG/JPEG) e devolve os metadados
// do arquivo. O diretório padrão é a galeria; optsJSON pode apontar outro
// diretório ABSOLUTO para exportações pontuais.
func (a *App) SaveScreenshotImage(dataURL, optsJSON string) (string, error) {
	opts, err := screenshot.ParseSaveImageOptions(optsJSON)
	if err != nil {
		return "", err
	}
	img, err := screenshot.DecodeDataURLToImage(dataURL)
	if err != nil {
		return "", fmt.Errorf("imagem invalida para salvar: %w", err)
	}
	format := opts.Format
	if format == "" {
		format = screenshot.ImageFormatFromDataURL(dataURL)
	}
	if format == "" {
		format = "png"
	}
	data, err := screenshot.EncodeImage(img, format, 0)
	if err != nil {
		return "", fmt.Errorf("nao foi possivel codificar a captura: %w", err)
	}

	dir := opts.Directory
	if dir == "" {
		dir, err = a.screenshotGalleryFolder()
		if err != nil {
			return "", err
		}
	} else {
		dir = filepath.Clean(dir)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("nao foi possivel criar o diretorio de destino: %w", err)
		}
	}

	fullPath, err := writeScreenshotFile(dir, opts.Prefix, format, data)
	if err != nil {
		return "", err
	}
	fileName := filepath.Base(fullPath)

	result := screenshot.SaveImageResult{
		Path:      fullPath,
		FileName:  fileName,
		Directory: dir,
		Bytes:     len(data),
		Width:     img.Bounds().Dx(),
		Height:    img.Bounds().Dy(),
		SHA256:    screenshot.SHA256Hex(data),
		Format:    format,
	}
	body, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	a.Logs.Append(fmt.Sprintf("[screenshot] captura salva em %s (%d bytes, %s)", fullPath, len(data), format))
	return string(body), nil
}

// writeScreenshotFile grava a captura SEM sobrescrever um arquivo existente e
// devolve o caminho final. os.WriteFile truncava a captura anterior quando o
// nome coincidia (mesmo segundo + mesmo sufixo aleatório — e o sufixo degenera
// para "0000" quando o gerador falha). Em colisão tenta de novo com outro
// sufixo; um arquivo parcial de escrita que falhou é removido.
func writeScreenshotFile(dir, prefix, format string, data []byte) (string, error) {
	at := time.Now()
	for attempt := 0; attempt < 5; attempt++ {
		suffix := screenshotRandomSuffix(4)
		if attempt > 0 {
			// Mantém o timestamp e varia o sufixo de forma determinística: assim o
			// retry progride mesmo que o gerador aleatório repita o valor.
			suffix = fmt.Sprintf("%s%d", suffix, attempt)
		}
		path := filepath.Join(dir, screenshot.BuildScreenshotFileName(prefix, at, suffix, format))
		if !screenshot.PathWithinDir(dir, path) {
			return "", fmt.Errorf("nome de arquivo de captura invalido")
		}
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("nao foi possivel gravar a captura: %w", err)
		}
		_, writeErr := file.Write(data)
		if closeErr := file.Close(); writeErr == nil {
			writeErr = closeErr
		}
		if writeErr != nil {
			_ = os.Remove(path)
			return "", fmt.Errorf("nao foi possivel gravar a captura: %w", writeErr)
		}
		return path, nil
	}
	return "", fmt.Errorf("nao foi possivel gerar um nome de arquivo livre para a captura em %q", dir)
}

// GetScreenshotEvidence devolve o SHA-256 do binário ORIGINAL da data URL
// (sem reencodar), dimensões e instante — base do "modo evidência".
func (a *App) GetScreenshotEvidence(dataURL string) (string, error) {
	raw, img, err := screenshot.DecodeDataURLImageBytes(dataURL)
	if err != nil {
		return "", fmt.Errorf("imagem invalida para evidencia: %w", err)
	}
	evidence := screenshot.BuildEvidence(raw, img, time.Now())
	body, err := json.Marshal(evidence)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// resolveScreenshotPathLinks resolve links simbólicos E junções de um caminho,
// componente a componente, e devolve o caminho efetivo no disco.
//
// filepath.EvalSymlinks não serve aqui: no Windows ele não segue junções (e
// devolve erro para qualquer caminho que passe por uma, o que fazia o guard
// pular a checagem). O resultado era o guard cego nos dois sentidos: uma junção
// DENTRO da galeria liberava ler/apagar arquivo de fora, e uma galeria que É
// junção (perfil redirecionado) resolvia o alvo para o caminho real e negava
// tudo. os.Readlink funciona nos dois casos; em caminho comum é no-op.
func resolveScreenshotPathLinks(path string, hops int) string {
	if hops > 32 {
		// Corrente de links absurda/em laço: devolve o caminho como veio.
		return path
	}
	link, err := os.Readlink(path)
	if err == nil {
		if !filepath.IsAbs(link) {
			link = filepath.Join(filepath.Dir(path), link)
		}
		return resolveScreenshotPathLinks(filepath.Clean(link), hops+1)
	}
	parent := filepath.Dir(path)
	if parent == path || parent == "." {
		return path
	}
	return filepath.Join(resolveScreenshotPathLinks(parent, hops), filepath.Base(path))
}

// ReadScreenshotImageFile lê uma imagem da galeria e devolve data URL. Recusa
// caminhos fora da pasta da galeria (inclusive via link simbólico/junção) e
// arquivos acima de maxBytes (0 = teto padrão), conferido ANTES de ler.
func (a *App) ReadScreenshotImageFile(path string, maxBytes int) (string, error) {
	gallery, err := a.screenshotGalleryFolder()
	if err != nil {
		return "", err
	}
	target, err := screenshot.ResolveGalleryPath(gallery, path)
	if err != nil {
		return "", err
	}
	if !screenshot.PathWithinDir(resolveScreenshotPathLinks(gallery, 0), resolveScreenshotPathLinks(target, 0)) {
		return "", fmt.Errorf("acesso negado: %q aponta para fora da pasta da galeria", path)
	}
	info, err := os.Stat(target)
	if err != nil {
		return "", fmt.Errorf("imagem da galeria nao encontrada: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("o caminho informado e uma pasta, nao uma imagem")
	}
	limit := maxBytes
	if limit <= 0 || limit > screenshot.MaxScreenshotFileBytes {
		limit = screenshot.MaxScreenshotFileBytes
	}
	if info.Size() > int64(limit) {
		return "", fmt.Errorf("imagem da galeria tem %d bytes, acima do limite de %d", info.Size(), limit)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return "", fmt.Errorf("nao foi possivel ler a imagem da galeria: %w", err)
	}
	if len(data) > limit {
		return "", fmt.Errorf("imagem da galeria tem %d bytes, acima do limite de %d", len(data), limit)
	}
	return "data:" + screenshotMimeForFile(target, data) + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// DeleteScreenshotImageFile apaga uma imagem da galeria. Idempotente: arquivo
// inexistente é sucesso (a UI pode repetir a exclusão sem erro).
func (a *App) DeleteScreenshotImageFile(path string) error {
	gallery, err := a.screenshotGalleryFolder()
	if err != nil {
		return err
	}
	target, err := screenshot.ResolveGalleryPath(gallery, path)
	if err != nil {
		return err
	}
	info, err := os.Lstat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("nao foi possivel consultar a imagem da galeria: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		// Apagar o link não apagaria a imagem, e seguir o link sairia da galeria.
		return fmt.Errorf("acesso negado: %q e um link simbolico", path)
	}
	if info.IsDir() {
		return fmt.Errorf("o caminho informado e uma pasta, nao uma imagem")
	}
	if !screenshot.PathWithinDir(resolveScreenshotPathLinks(gallery, 0), resolveScreenshotPathLinks(target, 0)) {
		return fmt.Errorf("acesso negado: %q aponta para fora da pasta da galeria", path)
	}
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("nao foi possivel apagar a imagem da galeria: %w", err)
	}
	a.Logs.Append("[screenshot] imagem removida da galeria: " + target)
	return nil
}

// GetScreenshotGalleryFolder devolve (e cria) a pasta da galeria.
func (a *App) GetScreenshotGalleryFolder() (string, error) {
	return a.screenshotGalleryFolder()
}

// screenshotMimeForFile escolhe o MIME pela extensão e confere o conteúdo: um
// arquivo renomeado não deve ser servido como PNG se for JPEG.
func screenshotMimeForFile(path string, data []byte) string {
	// Assinatura primeiro: a extensão é só uma dica e um arquivo renomeado não
	// pode ser servido como PNG se o conteúdo for JPEG (e vice-versa).
	if len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}) {
		return "image/png"
	}
	if len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return "image/jpeg"
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	default:
		return "image/png"
	}
}

// ocrBindingResult é o contrato JSON devolvido por OcrScreenshotImage.
type ocrBindingResult struct {
	Text            string `json:"text"`
	EngineAvailable bool   `json:"engineAvailable"`
	Error           string `json:"error"`
}

type ocrScriptOutput struct {
	OK              bool   `json:"ok"`
	EngineAvailable bool   `json:"engineAvailable"`
	Text            string `json:"text"`
	Language        string `json:"language"`
	Error           string `json:"error"`
}

// truncateUTF8Text corta o texto no limite SEM partir um caractere multi-byte.
// Cortar por byte podia terminar no meio de uma rune; o json.Marshal trocava o
// resto por U+FFFD e o texto chegava corrompido na UI.
func truncateUTF8Text(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

func encodeOCRResult(result ocrBindingResult) string {
	result.Text = truncateUTF8Text(result.Text, screenshot.OCRMaxOutputChars)
	body, err := json.Marshal(result)
	if err != nil {
		return `{"text":"","engineAvailable":false,"error":"falha ao serializar o resultado do OCR"}`
	}
	return string(body)
}

func ocrFailure(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		message = "OCR indisponivel"
	}
	return encodeOCRResult(ocrBindingResult{EngineAvailable: false, Error: message})
}

// parseOCRScriptOutput procura o último JSON de uma linha impresso pelo script.
// O PowerShell pode emitir avisos antes do resultado; só a linha JSON importa.
func parseOCRScriptOutput(stdout string) (ocrBindingResult, bool) {
	lines := strings.Split(stdout, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		// BOM (U+FEFF) NÃO é espaço para o unicode.IsSpace: sem removê-lo o
		// HasPrefix("{") falhava e um OCR bem-sucedido virava "nenhuma resposta".
		line := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[i]), "\ufeff"))
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var parsed ocrScriptOutput
		if err := json.Unmarshal([]byte(line), &parsed); err != nil {
			continue
		}
		if parsed.OK && strings.TrimSpace(parsed.Error) == "" {
			return ocrBindingResult{Text: parsed.Text, EngineAvailable: true}, true
		}
		message := strings.TrimSpace(parsed.Error)
		if message == "" {
			message = "o motor de OCR do Windows nao reconheceu a imagem"
		}
		return ocrBindingResult{EngineAvailable: parsed.EngineAvailable, Error: message}, true
	}
	return ocrBindingResult{}, false
}

// OcrScreenshotImage faz OCR on-device via Windows.Media.Ocr. Quando o motor ou
// o idioma não existem, degrada com engineAvailable=false e error descritivo —
// nunca falha o binding (a UI mostra o aviso em vez de perder a captura).
func (a *App) OcrScreenshotImage(dataURL, language string) (string, error) {
	_, img, err := screenshot.DecodeDataURLImageBytes(dataURL)
	if err != nil {
		return ocrFailure("imagem invalida para OCR: " + err.Error()), nil
	}
	tmp, err := os.CreateTemp("", "discovery-ocr-*.png")
	if err != nil {
		return ocrFailure("nao foi possivel criar o arquivo temporario do OCR: " + err.Error()), nil
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := png.Encode(tmp, img); err != nil {
		_ = tmp.Close()
		return ocrFailure("nao foi possivel preparar a imagem do OCR: " + err.Error()), nil
	}
	if err := tmp.Close(); err != nil {
		return ocrFailure("nao foi possivel fechar o arquivo temporario do OCR: " + err.Error()), nil
	}

	script := screenshot.BuildOCRPowerShellScript(tmpPath, language)
	ctx, cancel := context.WithTimeout(context.Background(), ocrPowerShellTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell.exe",
		"-NoLogo", "-NoProfile", "-NonInteractive",
		"-ExecutionPolicy", "Bypass", "-WindowStyle", "Hidden",
		"-Command", script,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return ocrFailure("OCR excedeu o tempo limite de 20 segundos"), nil
	}
	if result, ok := parseOCRScriptOutput(stdout.String()); ok {
		if runErr != nil && result.Error == "" {
			result.EngineAvailable = false
			result.Error = "o motor de OCR terminou com erro: " + runErr.Error()
		}
		return encodeOCRResult(result), nil
	}
	message := strings.TrimSpace(stderr.String())
	if message == "" && runErr != nil {
		message = runErr.Error()
	}
	if message == "" {
		message = "nenhuma resposta do motor de OCR do Windows"
	}
	return ocrFailure("falha ao executar o OCR: " + message), nil
}

// CaptureScrollingScreenshot captura uma página longa: esconde a janela do
// agente, tira o print inicial, rola com a roda do mouse e recaptura até o
// conteúdo parar de mudar ou atingir maxScrolls (1..20). Os frames são
// costurados por sobreposição em core/screenshot.
func (a *App) CaptureScrollingScreenshot(reason string, maxScrolls int) (string, error) {
	if maxScrolls <= 0 {
		maxScrolls = 5
	}
	if maxScrolls < scrollingMinScrolls {
		maxScrolls = scrollingMinScrolls
	}
	if maxScrolls > scrollingMaxScrolls {
		maxScrolls = scrollingMaxScrolls
	}

	restore := a.hideAgentWindowForCapture()
	defer restore()

	first, err := screenshot.CaptureDesktopFrame(0, 0, 0, 0)
	if err != nil {
		return "", fmt.Errorf("captura de rolagem: falha na captura inicial: %w", err)
	}
	if first == nil || first.Width <= 0 || first.Height <= 0 {
		return "", fmt.Errorf("captura de rolagem: a captura inicial veio vazia")
	}

	frames := []*screen.Frame{first}
	previous := first
	scrolls := 0
	for scrolls < maxScrolls {
		if err := screenshot.ScrollDown(scrollingScrollNotches); err != nil {
			if scrolls == 0 {
				return "", fmt.Errorf("captura de rolagem: %w", err)
			}
			a.Logs.Append("[screenshot] rolagem interrompida: " + err.Error())
			break
		}
		scrolls++
		time.Sleep(scrollingSettleDelay)
		next, captureErr := screenshot.CaptureDesktopFrame(0, 0, 0, 0)
		if captureErr != nil {
			a.Logs.Append("[screenshot] rolagem: falha ao recapturar: " + captureErr.Error())
			break
		}
		if next == nil || next.Width <= 0 || next.Height <= 0 {
			break
		}
		if screenshot.FramesIdentical(previous, next) {
			// Conteúdo chegou ao fim (ou a janela não rolou).
			break
		}
		frames = append(frames, next)
		previous = next
		if len(frames) >= 2 && len(frames)*first.Height >= screenshot.MaxStitchHeight+first.Height {
			// Já há material para atingir o teto de altura; mais frames só
			// custariam captura/CPU para serem descartados na costura.
			break
		}
	}

	// A sobreposição possível vai até a altura do frame: com três notches de roda
	// o conteúdo desce bem menos de uma tela, então buscar só uma faixa curta
	// deixaria de encontrar a continuidade e duplicaria o conteúdo.
	stitched, used := screenshot.StitchScrollFrames(frames, first.Height)
	if stitched == nil || stitched.Width <= 0 || stitched.Height <= 0 {
		return "", fmt.Errorf("captura de rolagem: nao foi possivel costurar os frames capturados")
	}
	data, mime, err := screenshot.EncodeFrame(stitched, 90, -1)
	if err != nil {
		return "", fmt.Errorf("captura de rolagem: falha ao codificar a imagem: %w", err)
	}
	// EncodeFrame pode reduzir a imagem para caber no teto de payload; as
	// dimensões do JSON precisam ser as do ARQUIVO entregue, não as do frame
	// costurado antes do encode.
	width, height := stitched.Width, stitched.Height
	if cfg, _, cfgErr := image.DecodeConfig(bytes.NewReader(data)); cfgErr == nil {
		width, height = cfg.Width, cfg.Height
	}
	payload := map[string]any{
		"imageDataUrl": "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data),
		"width":        width,
		"height":       height,
		"scrolls":      scrolls,
		"truncated":    stitched.Height >= screenshot.MaxStitchHeight,
		"stitched":     used >= 2,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	a.Logs.Append(fmt.Sprintf("[screenshot] captura de rolagem concluida (%dx%d, %d rolagens, motivo: %s)",
		width, height, scrolls, strings.TrimSpace(reason)))
	return string(body), nil
}
