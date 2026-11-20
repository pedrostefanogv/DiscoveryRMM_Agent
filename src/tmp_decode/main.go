package main

import (
	"encoding/hex"
	"fmt"
	"os"

	"discovery/app/core/remotesession/rdcleanpath"
)

func main() {
	raw, err := hex.DecodeString(os.Args[1])
	if err != nil {
		fmt.Println("hex invalido:", err)
		return
	}
	pdu, err := rdcleanpath.Decode(raw)
	if err != nil {
		fmt.Println("DECODE FALHOU:", err)
		return
	}
	fmt.Printf("version       = %d (esperado 3390)\n", pdu.Version)
	if pdu.Destination != nil {
		fmt.Printf("destination   = %q\n", *pdu.Destination)
	}
	if pdu.ProxyAuth != nil {
		fmt.Printf("proxy_auth    = %d chars\n", len(*pdu.ProxyAuth))
	}
	if pdu.PreconnectionBlob != nil {
		fmt.Printf("preconnection = %q\n", *pdu.PreconnectionBlob)
	}
	fmt.Printf("x224_pdu      = %d bytes: % x\n", len(pdu.X224ConnectionPdu), pdu.X224ConnectionPdu)
	fmt.Printf("error         = %v\n", pdu.Error)
	fmt.Println()
	fmt.Println("O REQUEST E VALIDO PARA O MEU PARSER.")
}
