package thirdparty

import "errors"

// ErrInvalidAppID indica identificador de app fora da allowlist.
var ErrInvalidAppID = errors.New("identificador de aplicativo invalido")
