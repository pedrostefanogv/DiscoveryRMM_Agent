//go:build windows

package native

import (
	"strings"

	"golang.org/x/sys/windows/registry"
)

// productTypeKeyPath é a chave canônica onde o Windows publica o tipo de
// produto da instalação (a MESMA que o "systeminfo" e o GetProductType()
// consultam). Diferente de ProductName/InstallationType — strings de exibição
// localizadas e sujeitas a remendos — este valor é estável desde o NT 4.0.
const productTypeKeyPath = `SYSTEM\CurrentControlSet\Control\ProductOptions`

// productTypeValueName é o nome do valor REG_SZ dentro de productTypeKeyPath.
const productTypeValueName = "ProductType"

// Valores possíveis de ProductType, exatamente como o Windows os publica.
const (
	// ProductTypeWorkstation = "WinNT": estação de trabalho (cliente).
	ProductTypeWorkstation = "WinNT"
	// ProductTypeServerNT = "ServerNT": servidor membro (não é DC).
	ProductTypeServerNT = "ServerNT"
	// ProductTypeLanmanNT = "LanmanNT": servidor controlador de domínio (DC).
	ProductTypeLanmanNT = "LanmanNT"
	// ProductTypeUnknown: não foi possível ler/interpretar o registro.
	ProductTypeUnknown = "unknown"
)

// normalizeProductType converte o valor bruto lido do registro em um dos
// valores canônicos acima. É PURA (não acessa o sistema) para poder ser
// testada sem registry.
//
// A comparação ignora caixa e espaços porque imagens customizadas já
// apareceram com variações de formatação — o significado não muda. Qualquer
// outra coisa (inclusive vazio) vira ProductTypeUnknown: nunca adivinhamos.
func normalizeProductType(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "winnt":
		return ProductTypeWorkstation
	case "servernt":
		return ProductTypeServerNT
	case "lanmannt":
		return ProductTypeLanmanNT
	default:
		return ProductTypeUnknown
	}
}

// isServerProductType informa se o ProductType canônico indica Windows Server
// (servidor membro OU controlador de domínio). É PURA.
func isServerProductType(productType string) bool {
	return productType == ProductTypeServerNT || productType == ProductTypeLanmanNT
}

// isDomainControllerProductType informa se o ProductType canônico indica um
// controlador de domínio. É PURA.
func isDomainControllerProductType(productType string) bool {
	return productType == ProductTypeLanmanNT
}

// readProductType lê ProductType do registro e devolve o valor normalizado.
//
// Best-effort: qualquer erro (chave ausente, permissão, tipo inesperado)
// resulta em ProductTypeUnknown — nunca panic e nunca erro propagado, pois
// isto roda no coletor de inventário, onde um dado ausente não pode derrubar
// a coleta inteira.
func readProductType() string {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, productTypeKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return ProductTypeUnknown
	}
	defer key.Close()

	raw, _, err := key.GetStringValue(productTypeValueName)
	if err != nil {
		return ProductTypeUnknown
	}
	return normalizeProductType(raw)
}

// detectProductType devolve o ProductType canônico da máquina atual
// ("WinNT" | "ServerNT" | "LanmanNT" | "unknown").
func detectProductType() string {
	return readProductType()
}

// IsWindowsServer informa se a máquina atual roda Windows Server (membro ou
// controlador de domínio), decidindo por ProductType.
//
// Exportada para pacotes fora do inventário (ex.: windowsupdate, que não deve
// criar pontos de restauração em servidor). Em cliente Windows retorna false,
// pois ProductType é "WinNT".
func IsWindowsServer() bool {
	return isServerProductType(detectProductType())
}
