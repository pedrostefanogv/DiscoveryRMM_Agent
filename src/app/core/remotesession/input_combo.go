//go:build windows

package remotesession

import (
	"fmt"
	"sort"
	"strings"

	"discovery/app/core/screen"
)

// ── Teclas especiais (combinações que o SO do VIEWER interceptaria) ──
//
// O viewer não consegue enviar Ctrl+Alt+Del, Win+L, Alt+Tab etc. apenas
// "digitando": essas combinações são capturadas pelo Windows LOCAL (Winlogon /
// shell) antes de chegarem ao navegador — o operador bloquearia a própria
// máquina, trocaria de janela local ou abriria o menu Iniciar local. Por isso a
// tela de acesso remoto oferece uma lista de combinações: o viewer envia um
// único evento (type "keycombo", combo "<id>") e o agent injeta a combinação no
// host remoto.
//
// Duas combinações NÃO são injetáveis por SendInput (o Windows as trata no
// kernel) e usam APIs dedicadas:
//   - ctrl+alt+del → SendSAS (sas.dll), dependente de SoftwareSASGeneration;
//   - win+l        → LockWorkStation (user32.dll).
//
// A allow-list é FECHADA: nomes fora dela são rejeitados com erro, e o
// resultado (ok/erro) é publicado ao viewer para feedback explícito.
var (
	injectSendSAS         = screen.SendSAS
	injectLockWorkstation = screen.LockWorkstation
)

// Métodos de injeção de uma combinação (também ecoados ao viewer no feedback).
const (
	comboMethodSequence = "sequence"
	comboMethodSAS      = "sas"
	comboMethodLock     = "lock"
)

// comboInjection descreve como uma combinação é injetada.
type comboInjection struct {
	Method string
	// Keys na ordem de PRESSÃO; a liberação é feita na ordem inversa (o último
	// pressionado é o primeiro liberado), como um pressionamento real.
	Keys []uint16
}

// VKs usados apenas pelas combinações (os demais vêm de input_controller.go).
const (
	VK_F4       = 0x73
	VK_SNAPSHOT = 0x2C
	VK_END      = 0x23
)

// specialKeyCombos é a allow-list canônica (ids usados pelo viewer).
var specialKeyCombos = map[string]comboInjection{
	// Segurança — exigem API dedicada (não passam por SendInput).
	"ctrl+alt+del": {Method: comboMethodSAS},
	"win+l":        {Method: comboMethodLock},

	// Gerenciamento / alternância de janelas.
	"ctrl+shift+esc": {Method: comboMethodSequence, Keys: []uint16{VK_CONTROL, VK_SHIFT, VK_ESCAPE}},
	"ctrl+esc":       {Method: comboMethodSequence, Keys: []uint16{VK_CONTROL, VK_ESCAPE}},
	"alt+tab":        {Method: comboMethodSequence, Keys: []uint16{VK_MENU, VK_TAB}},
	"alt+shift+tab":  {Method: comboMethodSequence, Keys: []uint16{VK_MENU, VK_SHIFT, VK_TAB}},
	"alt+f4":         {Method: comboMethodSequence, Keys: []uint16{VK_MENU, VK_F4}},
	"ctrl+alt+end":   {Method: comboMethodSequence, Keys: []uint16{VK_CONTROL, VK_MENU, VK_END}},

	// Shell do Windows.
	"win":     {Method: comboMethodSequence, Keys: []uint16{VK_LWIN}},
	"win+d":   {Method: comboMethodSequence, Keys: []uint16{VK_LWIN, 0x44}},
	"win+e":   {Method: comboMethodSequence, Keys: []uint16{VK_LWIN, 0x45}},
	"win+r":   {Method: comboMethodSequence, Keys: []uint16{VK_LWIN, 0x52}},
	"win+x":   {Method: comboMethodSequence, Keys: []uint16{VK_LWIN, 0x58}},
	"win+tab": {Method: comboMethodSequence, Keys: []uint16{VK_LWIN, VK_TAB}},

	// Captura de tela.
	"printscreen":     {Method: comboMethodSequence, Keys: []uint16{VK_SNAPSHOT}},
	"alt+printscreen": {Method: comboMethodSequence, Keys: []uint16{VK_MENU, VK_SNAPSHOT}},
}

// comboAliases normaliza sinônimos comuns digitados/enviados pelo viewer pelo
// mesmo id canônico (ex.: "control+alt+delete" e "ctrl+alt+del").
var comboAliases = map[string]string{
	"control":  "ctrl",
	"ctl":      "ctrl",
	"windows":  "win",
	"meta":     "win",
	"super":    "win",
	"cmd":      "win",
	"escape":   "esc",
	"delete":   "del",
	"prtsc":    "printscreen",
	"prtscrn":  "printscreen",
	"prntscrn": "printscreen",
	"print":    "printscreen",
}

// normalizeSpecialKey reduz um id de combinação à forma canônica: minúsculas,
// separadores "+" sem espaços, sinônimos mapeados e partes vazias descartadas.
func normalizeSpecialKey(raw string) string {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(raw)), "+")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if alias, ok := comboAliases[part]; ok {
			part = alias
		}
		out = append(out, part)
	}
	return strings.Join(out, "+")
}

// IsSupportedSpecialKey informa se o id (após normalização) está na allow-list.
func IsSupportedSpecialKey(raw string) bool {
	_, ok := specialKeyCombos[normalizeSpecialKey(raw)]
	return ok
}

// SupportedSpecialKeys devolve os ids canônicos da allow-list em ordem
// alfabética (determinístico — para docs, diagnóstico e testes).
func SupportedSpecialKeys() []string {
	keys := make([]string, 0, len(specialKeyCombos))
	for id := range specialKeyCombos {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	return keys
}

// SpecialKeyVirtualKeys devolve os VKs que a combinação injeta via SendInput.
// Vazio para as combinações de API dedicada (SAS/bloqueio), que não passam por
// SendInput. Usado pelo InputController para não manter no rastreamento teclas
// que a própria combinação acabou de pressionar/liberar (ver forgetTrackedKeys).
func SpecialKeyVirtualKeys(raw string) []uint16 {
	inj, ok := specialKeyCombos[normalizeSpecialKey(raw)]
	if !ok {
		return nil
	}
	return inj.Keys
}

// InjectSpecialKey injeta uma combinação da allow-list e devolve o método usado
// ("sequence", "sas" ou "lock"). Nomes desconhecidos são rejeitados — nunca há
// injeção de sequência arbitrária vinda do viewer por este caminho.
func InjectSpecialKey(raw string) (string, error) {
	canonical := normalizeSpecialKey(raw)
	inj, ok := specialKeyCombos[canonical]
	if !ok {
		return "", fmt.Errorf("combinação de teclas não suportada: %q", raw)
	}

	switch inj.Method {
	case comboMethodSAS:
		if err := injectSendSAS(); err != nil {
			return inj.Method, err
		}
		return inj.Method, nil
	case comboMethodLock:
		if err := injectLockWorkstation(); err != nil {
			return inj.Method, err
		}
		return inj.Method, nil
	}

	// Sequência SendInput: pressiona na ordem, libera na ordem inversa. Se um
	// keydown falhar, libera o que já foi pressionado para não deixar tecla
	// "presa" no remoto (o watchdog do InputController só cobre teclas do
	// viewer, não as desta injeção).
	pressed := 0
	for _, vk := range inj.Keys {
		if err := injectKeyDown(vk); err != nil {
			for i := pressed - 1; i >= 0; i-- {
				_ = injectKeyUp(inj.Keys[i])
			}
			return inj.Method, fmt.Errorf("key down VK=0x%X falhou: %w", vk, err)
		}
		pressed++
	}
	// Libera TODAS as teclas mesmo se um keyup falhar: parar no primeiro erro
	// deixaria as demais (inclusive modificadores) presas no remoto.
	var releaseErr error
	for i := len(inj.Keys) - 1; i >= 0; i-- {
		if err := injectKeyUp(inj.Keys[i]); err != nil && releaseErr == nil {
			releaseErr = fmt.Errorf("key up VK=0x%X falhou: %w", inj.Keys[i], err)
		}
	}
	return inj.Method, releaseErr
}
