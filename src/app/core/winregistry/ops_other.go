//go:build !windows

package winregistry

// Stubs nao-Windows.
//
// Existem para manter \`go vet ./...\` e \`gofmt\` verdes em qualquer host de
// build/CI, mesmo padrao de remotesession/rdp_preflight_other.go. Nenhuma
// operacao de registro faz sentido fora do Windows; devolver
// ErrUnsupportedPlatform e explicito e testavel.

// ListKey stub.
func ListKey(hive Hive, keyPath, token string) (*ListPage, error) {
	return nil, ErrUnsupportedPlatform
}

// StatKey stub.
func StatKey(hive Hive, keyPath string) (*KeyInfo, error) {
	return nil, ErrUnsupportedPlatform
}

// GetValue stub.
func GetValue(hive Hive, keyPath, name string) (*ValueEntry, error) {
	return nil, ErrUnsupportedPlatform
}

// SetValue stub.
func SetValue(hive Hive, keyPath, name string, vType ValueType, data string, confirmed bool) error {
	return ErrUnsupportedPlatform
}

// DeleteValue stub.
func DeleteValue(hive Hive, keyPath, name string, confirmed bool) error {
	return ErrUnsupportedPlatform
}

// CreateKey stub.
func CreateKey(hive Hive, keyPath string, confirmed bool) error {
	return ErrUnsupportedPlatform
}

// DeleteKey stub.
func DeleteKey(hive Hive, keyPath string, recursive, confirmed bool) error {
	return ErrUnsupportedPlatform
}

// RenameKey stub.
func RenameKey(hive Hive, keyPath, newName string, confirmed bool) error {
	return ErrUnsupportedPlatform
}

// ExportKey stub.
func ExportKey(hive Hive, keyPath string, recursive bool) (string, error) {
	return "", ErrUnsupportedPlatform
}
