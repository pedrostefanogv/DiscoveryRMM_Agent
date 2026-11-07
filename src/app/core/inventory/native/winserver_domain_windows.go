//go:build windows

package native

import "strings"

// computerSystemState é o subconjunto de Win32_ComputerSystem que o
// inventário precisa para descrever a participação da máquina em domínio.
// É uma struct própria (e não o mapa cru do WMI) para permitir testar a
// decisão de forma pura, sem COM/WMI.
type computerSystemState struct {
	// Domain traz o domínio DNS quando a máquina está em domínio, ou o nome
	// do grupo de trabalho quando não está — o WMI usa o MESMO campo para os
	// dois casos.
	Domain string
	// Workgroup é o nome do grupo de trabalho (preenchido fora de domínio).
	Workgroup string
	// PartOfDomain indica se a máquina é membro de um domínio AD/Entra.
	PartOfDomain bool
}

// domainState é o resultado normalizado de computerSystemState, pronto para
// ir ao modelo de inventário. Separar a decisão da leitura permite testar
// sem tocar no WMI.
type domainState struct {
	Domain       string
	Workgroup    string
	PartOfDomain bool
}

// resolveDomainState decide, a partir do estado cru do Win32_ComputerSystem,
// o que vai em Domain e o que vai em Workgroup. É PURA.
//
// Regra: o WMI preenche "Domain" com o domínio quando PartOfDomain é
// verdadeiro e com o grupo de trabalho quando é falso. Publicar um grupo de
// trabalho no campo Domain confundiria o servidor (que usa Domain para
// agrupar e para GPO), então normalizamos: em cliente não ingressado, Domain
// fica VAZIO e o grupo de trabalho vai para Workgroup. Se o WMI já trouxe
// Workgroup preenchido, ele tem precedência.
//
// Best-effort: sem dados, devolve tudo vazio/false (nunca inventa).
func resolveDomainState(state computerSystemState) domainState {
	domain := strings.TrimSpace(state.Domain)
	workgroup := strings.TrimSpace(state.Workgroup)

	if state.PartOfDomain {
		return domainState{
			Domain:       domain,
			Workgroup:    workgroup,
			PartOfDomain: true,
		}
	}

	// Fora de domínio: se Workgroup veio vazio mas Domain trouxe um nome,
	// esse nome É o grupo de trabalho (comportamento do Win32_ComputerSystem).
	if workgroup == "" {
		workgroup = domain
	}
	return domainState{
		Domain:       "",
		Workgroup:    workgroup,
		PartOfDomain: false,
	}
}

// readComputerSystemState lê Domain, Workgroup e PartOfDomain de
// Win32_ComputerSystem via WMI nativo (go-ole) — o mesmo caminho dos outros
// coletores de inventário, sem PowerShell e sem parsing de texto.
//
// Best-effort: erro do WMI devolve estado vazio sem propagar falha, para não
// derrubar a coleta de sistema inteira por causa de um único campo.
func readComputerSystemState() computerSystemState {
	rows, err := wmiQuery("root\\cimv2", "SELECT Domain, Workgroup, PartOfDomain FROM Win32_ComputerSystem")
	if err != nil || len(rows) == 0 {
		return computerSystemState{}
	}
	row := rows[0]
	// PartOfDomain é booleano VB: normalizeWMIValue devolve int64 (1/0).
	return computerSystemState{
		Domain:       wmiString(row, "Domain"),
		Workgroup:    wmiString(row, "Workgroup"),
		PartOfDomain: wmiInt(row, "PartOfDomain") != 0,
	}
}

// detectDomainState lê e normaliza o estado de domínio em uma chamada.
func detectDomainState() domainState {
	return resolveDomainState(readComputerSystemState())
}
