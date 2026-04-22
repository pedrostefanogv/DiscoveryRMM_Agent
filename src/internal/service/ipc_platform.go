package service

import "net"

type serviceConn interface {
	net.Conn
}
