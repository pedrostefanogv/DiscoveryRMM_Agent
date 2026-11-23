//go:build windows

package native

import "testing"

// TestDetectOSIdentity_Contrato garante a coerência interna da identidade do SO
// resolvida nativamente (registro ProductOptions + WMI), em QUALQUER máquina:
//
//   - ProductType é sempre um valor canônico ou "unknown";
//   - IsServerKnown só é true quando o ProductType foi lido de fato;
//   - IsServer reflete exatamente ServerNT/LanmanNT (nunca um "false" para
//     leitura que falhou);
//   - ProductType desconhecido nunca vem acompanhado de IsServer=true.
//
// É o contrato que o envelope do agente usa para decidir entre reportar
// osIsServer ou OMITIR o campo (não reportado).
func TestDetectOSIdentity_Contrato(t *testing.T) {
	identity := DetectOSIdentity()

	switch identity.ProductType {
	case ProductTypeWorkstation, ProductTypeServerNT, ProductTypeLanmanNT, ProductTypeUnknown:
	default:
		t.Fatalf("ProductType = %q, fora do vocabulário canônico", identity.ProductType)
	}

	wantKnown := identity.ProductType != ProductTypeUnknown
	if identity.IsServerKnown != wantKnown {
		t.Fatalf("IsServerKnown = %v, esperado %v para ProductType %q", identity.IsServerKnown, wantKnown, identity.ProductType)
	}

	wantServer := identity.ProductType == ProductTypeServerNT || identity.ProductType == ProductTypeLanmanNT
	if identity.IsServer != wantServer {
		t.Fatalf("IsServer = %v, esperado %v para ProductType %q", identity.IsServer, wantServer, identity.ProductType)
	}

	if !identity.IsServerKnown && identity.IsServer {
		t.Fatalf("leitura desconhecida não pode afirmar IsServer=true")
	}
}

// TestDetectOSIdentity_NaoDependeDeOsquery documenta e trava o ponto central:
// a identidade vem de fontes nativas (registro/WMI). Nesta máquina Windows ela
// precisa ser CONHECIDA — se este teste falhar, o registro não está acessível e
// o agente passará a omitir os campos (não reportado), nunca a afirmar "cliente".
func TestDetectOSIdentity_NaoDependeDeOsquery(t *testing.T) {
	if identity := DetectOSIdentity(); !identity.IsServerKnown {
		t.Fatalf("ProductType = %q: leitura do registro falhou nesta máquina Windows; "+
			"o agente enviará os campos de identidade como NÃO REPORTADOS", identity.ProductType)
	}
}
