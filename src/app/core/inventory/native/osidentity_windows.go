//go:build windows

package native

// DetectOSIdentity resolve a identidade do SO pelas fontes canônicas nativas:
//
//   - ProductType do registro (SYSTEM\CurrentControlSet\Control\ProductOptions),
//     a mesma chave que o systeminfo usa — estável desde o NT 4.0, diferente de
//     ProductName/InstallationType (strings de exibição localizadas);
//   - Win32_ComputerSystem via WMI nativo (go-ole) para Domain/Workgroup/
//     PartOfDomain.
//
// Sem osquery, sem PowerShell e sem subprocesso: por isso a identidade continua
// correta mesmo quando o inventário foi coletado pelo caminho osquery (que não
// traz nenhum desses campos).
//
// Best-effort: falha de leitura vira ProductType "unknown" com IsServerKnown
// false — nunca um "false" inventado.
func DetectOSIdentity() OSIdentity {
	productType := detectProductType()
	state := detectDomainState()

	return OSIdentity{
		ProductType:   productType,
		IsServer:      isServerProductType(productType),
		IsServerKnown: productType != ProductTypeUnknown,
		Domain:        state.Domain,
		Workgroup:     state.Workgroup,
		PartOfDomain:  state.PartOfDomain,
	}
}
