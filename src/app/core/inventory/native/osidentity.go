package native

// ProductTypeUnknown indica que NÃO foi possível ler/interpretar o tipo de
// produto da instalação no registro. Fica num arquivo neutro de plataforma
// porque a identidade do SO é consultada também fora do Windows — onde o valor
// é sempre "unknown" e o chamador NÃO pode afirmar que a máquina não é servidor.
const ProductTypeUnknown = "unknown"

// OSIdentity é a identidade do SO resolvida NATIVAMENTE: ProductType do
// registro (ProductOptions) e domínio/grupo de trabalho do Win32_ComputerSystem
// via WMI. Não depende de osquery, PowerShell nem de parsing de string de
// exibição — funciona igualmente quando o INVENTÁRIO veio pelo caminho osquery.
//
// IsServerKnown distingue "não é servidor" de "não deu para ler". Quem consome
// precisa OMITIR o campo quando for false (não reportado) em vez de afirmar
// false: uma leitura falha do registro não pode transformar um Windows Server
// real em estação de trabalho no painel (e, no caminho do agente, também não
// pode transformar um servidor com bateria em candidato a notebook).
type OSIdentity struct {
	// ProductType canônico: "WinNT" | "ServerNT" | "LanmanNT" | "unknown".
	ProductType string
	// IsServer informa servidor membro OU controlador de domínio.
	IsServer bool
	// IsServerKnown é false quando ProductType é "unknown" (não reportado).
	IsServerKnown bool
	// Domain é o domínio DNS quando a máquina está em domínio (vazio fora).
	Domain string
	// Workgroup é o grupo de trabalho quando a máquina NÃO está em domínio.
	Workgroup string
	// PartOfDomain informa se a máquina é membro de domínio AD/Entra.
	PartOfDomain bool
}
