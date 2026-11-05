//go:build windows

package processutil

import (
	"fmt"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	userenvDLL                  = windows.NewLazySystemDLL("userenv.dll")
	procCreateEnvironmentBlock  = userenvDLL.NewProc("CreateEnvironmentBlock")
	procDestroyEnvironmentBlock = userenvDLL.NewProc("DestroyEnvironmentBlock")
)

// BuildUserEnvironment calls CreateEnvironmentBlock (userenv.dll) to build the
// complete set of environment variables for the given user token.
//
// The returned slice has the form ["KEY=VALUE", ...] and is suitable for
// assignment to cmd.Env. This ensures child processes spawned via
// CreateProcessAsUser see correct user-specific variables such as
// %LOCALAPPDATA%, %APPDATA%, %USERPROFILE%, %TEMP%, etc., rather than
// inheriting the service's SYSTEM-scoped environment.
//
// On failure, an error is returned; callers should fall back to the inherited
// environment (service env) rather than propagating the error.
func BuildUserEnvironment(tok syscall.Token) ([]string, error) {
	// Use unsafe.Pointer (not uintptr) so that the block pointer returned by
	// the OS API can be passed back to parseEnvBlock without triggering
	// go vet's "possible misuse of unsafe.Pointer" rule (uintptr→unsafe.Pointer).
	var block unsafe.Pointer
	r, _, err := procCreateEnvironmentBlock.Call(
		uintptr(unsafe.Pointer(&block)),
		uintptr(tok),
		0, // bInherit = FALSE — do not merge the service's environment
	)
	if r == 0 {
		return nil, fmt.Errorf("CreateEnvironmentBlock: %w", err)
	}
	defer procDestroyEnvironmentBlock.Call(uintptr(block)) //nolint:errcheck

	return parseEnvBlock((*uint16)(block)), nil
}

// parseEnvBlock converts a Windows multi-string environment block (a contiguous
// sequence of null-terminated UTF-16 strings terminated by an extra null word)
// into a []string of "KEY=VALUE" entries.
func parseEnvBlock(block *uint16) []string {
	if block == nil {
		return nil
	}
	// Percorre o bloco com aritmética de ponteiro, avançando UMA palavra por
	// vez e parando no terminador de duplo nulo.
	//
	// NÃO converter o bloco para um array grande (ex.: *[1<<20]uint16): essa
	// conversão DECLARA 2 MB de memória a partir de um ponteiro que pode
	// apontar para uma alocação de poucos bytes. Isso é comportamento
	// indefinido (leitura fora dos limites) e o runtime o detecta sob
	// -race: "checkptr: converted pointer straddles multiple allocations"
	// (reproduzido em TestParseEnvBlock_EmptyBlock, com bloco vazio).
	var entries []string
	// maxEnvWords limita a varredura: um bloco corrompido sem duplo nulo não
	// pode fazer o laço percorrer memória indefinidamente.
	const maxEnvWords = 1 << 20
	for w := 0; w < maxEnvWords; {
		// Coleta uma string terminada em nulo a partir de block[w].
		start := w
		for {
			ch := *(*uint16)(unsafe.Add(unsafe.Pointer(block), uintptr(w)*unsafe.Sizeof(uint16(0))))
			if ch == 0 {
				break
			}
			w++
		}
		if w == start {
			break // duplo nulo = fim do bloco
		}
		// Constrói a string UTF-16 sem materializar uma slice do array inteiro:
		// copia só o trecho desta entrada.
		unit := make([]uint16, w-start)
		for k := range unit {
			unit[k] = *(*uint16)(unsafe.Add(unsafe.Pointer(block), uintptr(start+k)*unsafe.Sizeof(uint16(0))))
		}
		entries = append(entries, string(utf16.Decode(unit)))
		w++ // pula o terminador da entrada
	}
	return entries
}
