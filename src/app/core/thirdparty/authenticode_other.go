//go:build !windows

package thirdparty

// verifyAuthenticode nao se aplica fora do Windows (sem Authenticode).
func verifyAuthenticode(string) error {
	return nil
}
