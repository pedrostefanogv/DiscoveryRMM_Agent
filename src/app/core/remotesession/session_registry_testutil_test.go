//go:build windows

package remotesession

import (
	"errors"
	"fmt"
)

// errUnmapped e um erro sem sentinela correspondente, usado para verificar que
// a classificacao cai em "internal" em vez de devolver string vazia.
var errUnmapped = errors.New("erro nao mapeado")

// wrapErr embrulha um sentinela com contexto, como o guard faz ao devolver a
// recusa (fmt.Errorf("%w: motivo", ErrProtectedKey)).
func wrapErr(sentinel error, msg string) error {
	return fmt.Errorf("%w: %s", sentinel, msg)
}
