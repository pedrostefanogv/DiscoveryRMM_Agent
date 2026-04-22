//go:build !windows

package service

import "net"

// applyServerSideIdentity em plataformas não-Windows mantém os valores declarados
// pelo cliente (Named Pipe impersonation é exclusivo do Windows).
// Em desenvolvimento/Linux, os campos UserSID/UserName do request não são alterados.
func applyServerSideIdentity(conn net.Conn, req *ClientRequest) {}

// promoteTokenForAction é no-op em plataformas não-Windows.
func promoteTokenForAction(reqID, actionID string) {}
