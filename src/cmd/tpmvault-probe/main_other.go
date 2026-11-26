//go:build !windows

// Command tpmvault-probe é um probe de viabilidade do cofre TPM 2.0 do agente
// Discovery. Em sistemas que não sejam Windows ele não tem implementação: o
// TPM é acessado via TBS (tbs.dll), então este stub existe apenas para manter
// o pacote compilável em linux/darwin.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "tpmvault-probe: disponível apenas no Windows (acesso ao TPM via TBS)")
	os.Exit(1)
}
