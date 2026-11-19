package winregistry

import (
	"path"
	"strings"
)

// MaxPathLen e o limite de tamanho de um caminho de chave.
//
// O registro do Windows aceita 255 caracteres POR componente, e o caminho
// completo pode passar de 16.000. Adotamos um teto conservador: acima disto a
// resposta nao cabe com folga no envelope NATS e, na pratica, e sinal de path
// montado por script em loop. Recusar com erro claro e melhor que truncar.
const MaxPathLen = 1024

// MaxValueNameLen espelha o limite do Windows para o nome de um valor.
const MaxValueNameLen = 16383

// NormalizePath valida e normaliza um caminho de chave DENTRO de um hive.
//
// Regras (todas fail-closed — qualquer duvida recusa):
//   - nao pode ser vazio nem so espacos;
//   - nao pode ser absoluto nem comecar com separador;
//   - nao pode conter "..", ".", nem componente vazio ("A\\B");
//   - nao pode conter NUL ou caracteres de controle;
//   - nao pode passar de MaxPathLen.
//
// NAO rejeita a barra invertida interna: ela e parte legitima do nome de uma
// chave (ex.: nomes de CLSID e chaves com "\\" no meio). O separador aceito e
// "\\" — a barra normal "/" e convertida, porque o console web tende a
// normalizar caminhos assim e recusar seria hostil sem ganho de seguranca.
func NormalizePath(raw string) (string, error) {
	// Controla ANTES de aparar: TrimSpace removeria "\n" e "\t" das pontas, e
	// um caminho com newline embutido (vindo de script ou de copia suja) seria
	// aceito silenciosamente apontando para outra chave.
	if strings.ContainsRune(raw, 0) || hasControlChars(raw) {
		return "", ErrInvalidPath
	}

	p := strings.TrimSpace(raw)
	if p == "" {
		// Chave raiz do hive: legitimo (ex.: listar HKLM).
		return "", nil
	}

	// Barra normal -> barra invertida (normalizacao, nao semantica).
	p = strings.ReplaceAll(p, "/", "\\")

	// Corta separadores nas pontas: "\SOFTWARE\" vira "SOFTWARE". Sem isto um
	// componente vazio no fim passaria pela checagem de componente abaixo.
	p = strings.Trim(p, "\\")
	if p == "" {
		return "", nil
	}

	if len(p) > MaxPathLen {
		return "", ErrInvalidPath
	}

	// path.Clean com separador invertido: usamos path (que trabalha com "/")
	// apos trocar para "/" temporariamente, para herdar a logica de "." e ".."
	// ja testada da biblioteca padrao em vez de reimplementar.
	probe := strings.ReplaceAll(p, "\\", "/")
	cleaned := path.Clean(probe)
	// path.Clean remove ".." resolvendo-o; se o resultado ainda tiver ".." no
	// inicio, o caminho tentava escapar da raiz.
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", ErrInvalidPath
	}
	// Se o caminho original continha "..", recusamos mesmo que Clean o resolva:
	// "SOFTWARE\\..\\SYSTEM" e um path que o operador nao quis digitar, e
	// aceitar a resolucao esconderia um erro de montagem do chamador.
	for _, comp := range strings.Split(p, "\\") {
		if comp == ".." || comp == "." {
			return "", ErrInvalidPath
		}
	}

	cleaned = strings.TrimPrefix(cleaned, "/")
	return strings.ReplaceAll(cleaned, "/", "\\"), nil
}

// hasControlChars rejeita caracteres de controle (< 0x20).
func hasControlChars(s string) bool {
	for _, r := range s {
		if r < 0x20 {
			return true
		}
	}
	return false
}

// ValidateValueName valida o nome de um valor.
//
// O nome VAZIO e valido e tem significado proprio no registro: e o valor
// "(Default)" de uma chave. Trata-lo como erro quebraria a leitura da maioria
// das chaves do Windows.
func ValidateValueName(name string) error {
	if strings.ContainsRune(name, 0) {
		return ErrInvalidPath
	}
	if len(name) > MaxValueNameLen {
		return ErrInvalidPath
	}
	return nil
}

// SplitFullPath separa "HKLM\\SOFTWARE\\Foo" em hive + caminho.
//
// Aceita tambem hive recebido em campo separado (que e o formato canonico do
// protocolo); este helper existe para os testes e para o export, que trabalham
// com o caminho completo como o usuario o le.
func SplitFullPath(full string) (Hive, string, error) {
	trimmed := strings.Trim(strings.TrimSpace(full), "\\")
	if trimmed == "" {
		return "", "", ErrInvalidHive
	}
	parts := strings.SplitN(trimmed, "\\", 2)
	hive, ok := ParseHive(parts[0])
	if !ok {
		return "", "", ErrInvalidHive
	}
	rest := ""
	if len(parts) == 2 {
		rest = parts[1]
	}
	normalized, err := NormalizePath(rest)
	if err != nil {
		return "", "", err
	}
	return hive, normalized, nil
}

// FullPath remonta "HKLM\\SOFTWARE\\Foo" a partir das partes canonicas.
func FullPath(hive Hive, p string) string {
	if p == "" {
		return string(hive)
	}
	return string(hive) + "\\" + p
}
