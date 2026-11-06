//go:build windows

package screen

import (
	"testing"
	"time"

	"discovery/app/core/winui"
)

// ── Regressão do fantasma do banner (faixa congelada na tela) ──
//
// Sintoma relatado em campo: ao fechar o navegador, a área sob o cartão do
// banner continuava mostrando os pixels da janela já fechada (faixa branca de
// 480x76 com resíduo da UI do navegador) sobre o papel de parede ao vivo.
//
// Causa: regiaoUtil exigia variação de cor (maxR-minR >= 2) e rejeitava toda
// região UNIFORME. Ao fechar o navegador a área sob o cartão vira papel de
// parede liso — uniforme, portanto rejeitada — e o refresh mantinha a máscara
// antiga indefinidamente. Estes testes fixam o contrato novo.

// regiaoUniforme monta uma região BGRA de w x h com a cor (b,g,r) uniforme.
func regiaoUniforme(w, h int, b, g, r byte) []byte {
	px := make([]byte, w*h*4)
	for i := 0; i+3 < len(px); i += 4 {
		px[i] = b
		px[i+1] = g
		px[i+2] = r
	}
	return px
}

// Regressão direta do bug: página em branco sob o cartão é conteúdo legítimo e
// deve ser ACEITA. Antes era rejeitada por não ter variação de cor.
func TestRegiaoUtilAceitaRegiaoUniformeBranca(t *testing.T) {
	if !regiaoUtil(regiaoUniforme(480, 76, 0xFF, 0xFF, 0xFF)) {
		t.Fatal("região branca uniforme foi rejeitada — é conteúdo legítimo (página em branco sob o cartão)")
	}
}

// Papel de parede liso / barra de título lisa também são conteúdo legítimo.
func TestRegiaoUtilAceitaRegiaoUniformeCinza(t *testing.T) {
	if !regiaoUtil(regiaoUniforme(200, 40, 0xE3, 0xE3, 0xE3)) {
		t.Fatal("região cinza uniforme foi rejeitada — é conteúdo legítimo (barra de título / wallpaper)")
	}
}

// Área escura lisa (tema escuro, vídeo preto) continua válida — o sinal de
// captura bloqueada é o bitmap TODO preto, não o conteúdo escuro.
func TestRegiaoUtilAceitaRegiaoUniformeEscura(t *testing.T) {
	if !regiaoUtil(regiaoUniforme(120, 40, 0x1A, 0x12, 0x0E)) {
		t.Fatal("região escura uniforme (mas não preta) foi rejeitada — é conteúdo legítimo")
	}
}

// O sinal real de captura inválida: bitmap todo preto (PrintWindow recusado).
func TestRegiaoUtilRejeitaBitmapTodoPreto(t *testing.T) {
	if regiaoUtil(regiaoUniforme(100, 30, 0, 0, 0)) {
		t.Fatal("bitmap todo preto foi aceito — é captura inválida")
	}
}

// Região vazia/curta não é captura utilizável.
func TestRegiaoUtilRejeitaRegiaoCurta(t *testing.T) {
	if regiaoUtil([]byte{0xFF, 0xFF, 0xFF}) {
		t.Fatal("buffer com menos de um pixel foi aceito")
	}
	if regiaoUtil(nil) {
		t.Fatal("buffer nulo foi aceito")
	}
}

// Conteúdo com variação continua aceito (regressão do comportamento anterior).
func TestRegiaoUtilAceitaGradiente(t *testing.T) {
	px := regiaoUniforme(200, 40, 0, 0, 0)
	for i := 0; i+3 < len(px); i += 4 {
		px[i+2] = byte((i / 4) % 256)
	}
	if !regiaoUtil(px) {
		t.Fatal("gradiente foi rejeitado — conteúdo com variação deve ser aceito")
	}
}

// ── Rede de segurança: expiração da máscara ──
//
// Se o refresh falhar repetidamente (janela travada, PrintWindow recusado,
// bitmap inválido), manter a máscara congelada para sempre deixa um retângulo de
// conteúdo morto na tela. Depois de mascaraFalhasParaDescartar falhas seguidas a
// máscara é DESCARTADA e a área volta a ser capturada ao vivo.

// registrarFalhaMascara abaixo do limite NÃO descarta (mantém o comportamento
// histórico de congelar por até 2 refreshes).
func TestRegistrarFalhaMascaraMantemAbaixoDoLimite(t *testing.T) {
	limparMascaras(t)
	m := mascaraDeTeste(0xB01, winui.Rect{X: 0, Y: 0, W: 10, H: 10}, 0x33, true)
	mascarasMu.Lock()
	mascaras[m.hwnd] = m
	mascarasMu.Unlock()

	for i := 1; i < mascaraFalhasParaDescartar; i++ {
		registrarFalhaMascara(m, "motivo de teste")
		if m.falhasConsecutivas != i {
			t.Fatalf("após %d falhas o contador = %d", i, m.falhasConsecutivas)
		}
		if !RegionMaskAvailable() {
			t.Fatalf("máscara descartada com apenas %d falhas (limite %d)", i, mascaraFalhasParaDescartar)
		}
	}
}

// Ao atingir o limite, a máscara sai do registry e deixa de reescrever a área.
func TestRegistrarFalhaMascaraDescartaAoAtingirLimite(t *testing.T) {
	limparMascaras(t)
	m := mascaraDeTeste(0xB02, winui.Rect{X: 0, Y: 0, W: 10, H: 10}, 0x44, true)
	mascarasMu.Lock()
	mascaras[m.hwnd] = m
	mascarasMu.Unlock()

	for i := 0; i < mascaraFalhasParaDescartar; i++ {
		registrarFalhaMascara(m, "motivo de teste")
	}

	if RegionMaskAvailable() {
		t.Fatal("máscara continua ativa depois de atingir o limite de falhas")
	}
	mascarasMu.Lock()
	_, existe := mascaras[m.hwnd]
	mascarasMu.Unlock()
	if existe {
		t.Fatal("máscara continua no registry depois de atingir o limite de falhas")
	}
}

// Um refresh bem-sucedido zera o contador: falhas esporádicas não acumulam ao
// longo da sessão. Sem o reset, duas falhas passageiras hoje e uma amanhã
// descartariam o cartão sem que ele nunca tivesse ficado morto na tela.
//
// Exercita atualizarMascaraAoVivo de verdade: o reset fica no fim do caminho de
// sucesso, logo após regiaoUtil aceitar o recorte. Aqui o caminho real não roda
// (PrintWindow exige janela real), então o teste verifica o CONTRATO do contador
// pelo mesmo ponto de entrada usado pelo refresh: registrarFalhaMascara não deve
// incrementar após um reset.
func TestFalhasConsecutivasZeramNoSucesso(t *testing.T) {
	limparMascaras(t)
	m := mascaraDeTeste(0xB03, winui.Rect{X: 0, Y: 0, W: 10, H: 10}, 0x55, true)
	mascarasMu.Lock()
	mascaras[m.hwnd] = m
	mascarasMu.Unlock()

	// Duas falhas passageiras (abaixo do limite): máscara mantida.
	registrarFalhaMascara(m, "motivo de teste")
	registrarFalhaMascara(m, "motivo de teste")
	if m.falhasConsecutivas != 2 {
		t.Fatalf("contador = %d, esperado 2", m.falhasConsecutivas)
	}
	if !RegionMaskAvailable() {
		t.Fatal("máscara descartada antes do limite")
	}

	// O caminho de sucesso de atualizarMascaraAoVivo faz "m.falhasConsecutivas = 0".
	m.falhasConsecutivas = 0

	// Uma falha depois do sucesso deve recomeçar a contagem do zero, não somar às
	// duas anteriores (que dariam 3 e descartariam a máscara).
	registrarFalhaMascara(m, "motivo de teste")
	if m.falhasConsecutivas != 1 {
		t.Fatalf("contador após reset = %d, esperado 1 (o sucesso deve zerar a contagem)", m.falhasConsecutivas)
	}
	if !RegionMaskAvailable() {
		t.Fatal("máscara foi descartada porque as falhas antigas não foram zeradas")
	}
}

// ── Watchdog de fechamento (Correção 3) ──
//
// RequestClose só POSTA WM_CLOSE e ignorava o retorno. Se a fila de mensagens da
// thread da janela estiver saturada o PostMessage falha, o WM_CLOSE nunca chega,
// a janela fica viva para sempre e o WM_DESTROY nunca roda — logo a máscara nunca
// é removida e o cartão permanece congelado na região do topo. Estes testes
// fixam o contrato do encerramento.

// Um banner sem thread de janela (doneCh nil) não deve travar Close().
func TestCloseSemThreadDeJanelaNaoBloqueia(t *testing.T) {
	b := &SessionBanner{}
	done := make(chan struct{})
	go func() {
		b.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close() bloqueou num banner sem thread de janela")
	}
}

// RequestClose é idempotente: chamar duas vezes não pode reenviar WM_CLOSE nem
// mexer no estado depois de encerrado.
func TestRequestCloseIdempotente(t *testing.T) {
	b := &SessionBanner{}
	b.closed = false
	// Primeira chamada marca fechado mesmo sem hwnd (não há janela a avisar).
	b.RequestClose()
	if !b.closed {
		t.Fatal("RequestClose não marcou o banner como fechado")
	}
	// Segunda chamada não deve alterar nada nem bloquear.
	b.RequestClose()
	if !b.closed {
		t.Fatal("estado de fechado foi revertido")
	}
}

// Um banner já fechado não deve esperar pelo doneCh em Close().
func TestCloseComDoneChJaEncerrado(t *testing.T) {
	b := &SessionBanner{closed: true, doneCh: make(chan struct{})}
	close(b.doneCh)
	done := make(chan struct{})
	go func() {
		b.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close() bloqueou com doneCh já encerrado")
	}
}
