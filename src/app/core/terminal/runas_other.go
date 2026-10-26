//go:build !windows

package terminal

// RunAs é o pedido de identidade do shell do terminal ("system"/"user").
// Em não-Windows só existe a identidade herdada do processo do agente — os
// stubs mantêm a API compilável e o comportamento inalterado.
type RunAs string

const (
	RunAsInherit RunAs = ""
	RunAsSystem  RunAs = "system"
	RunAsUser    RunAs = "user"
)

// ParseRunAs mapeia qualquer valor para RunAsInherit fora do Windows.
func ParseRunAs(s string) RunAs { return RunAsInherit }

// ValidRunAs aceita os mesmos valores do Windows (validação de contrato).
func ValidRunAs(s string) bool {
	switch s {
	case "", "inherit", "system", "user":
		return true
	}
	return false
}

// EffectiveRunAs devolve "inherit" fora do Windows.
func EffectiveRunAs(RunAs) string { return "inherit" }
