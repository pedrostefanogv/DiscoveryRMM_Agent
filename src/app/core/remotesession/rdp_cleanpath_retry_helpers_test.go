package remotesession

import "errors"

// Helpers de teste para isAwaitRequestTimeout.
func errAwaitRequestForTest() error {
	return errors.New("rdcleanpath: timeout aguardando request (15s)")
}

func errOtherForTest() error {
	return errors.New("rdcleanpath: handshake TLS falhou")
}
