// Package rdcleanpath implementa o codec ASN.1 DER do protocolo RDCleanPath
// (VERSION_1), usado pelo IronRDP e pelo Devolutions Gateway para transporte
// de RDP sobre WebSocket.
//
// Referencia autoritativa:
//
//	https://github.com/Devolutions/IronRDP/blob/master/crates/ironrdp-rdcleanpath/src/lib.rs
//
// IMPORTANTE: o PDU e um SEQUENCE com `tag_mode = EXPLICIT` e todos os campos
// sao context-specific opcionais. Decoders derivados de `der` rejeitam campos
// desconhecidos, portanto NUNCA adicione campos novos a este PDU.
package rdcleanpath

// BASE_VERSION e o valor base; VERSION_1 e o unico versionamento em uso.
const (
	BASEVersion uint64 = 3389
	Version1    uint64 = BASEVersion + 1 // 3390
)

// Codigos de erro do RDCleanPath. Os codigos 100..105 sao "standard reasons"
// e NAO podem ser renumerados nem reutilizados (peers ja implantados dependem
// desses numeros). 16384..32767 sao implementation-specific.
const (
	ErrGeneralError        uint16 = 1
	ErrNegotiationError    uint16 = 2
	ErrExpired             uint16 = 100
	ErrNotYetValid         uint16 = 101
	ErrRevoked             uint16 = 102
	ErrTargetUnreachable   uint16 = 103
	ErrDNSResolutionFailed uint16 = 104
	ErrTLSHandshakeFailed  uint16 = 105
)

// Tags context-specific EXPLICIT do RDCleanPathPdu.
const (
	tagVersion           = 0
	tagError             = 1
	tagDestination       = 2
	tagProxyAuth         = 3
	tagServerAuth        = 4
	tagPreconnectionBlob = 5
	tagX224ConnectionPdu = 6
	tagServerCertChain   = 7
	tagServerAddr        = 9
)

// Tags context-specific EXPLICIT do RDCleanPathErr.
const (
	errTagErrorCode    = 0
	errTagHTTPStatus   = 1
	errTagWSALastError = 2
	errTagTLSAlertCode = 3
)

// Err é o "proxy error" do RDCleanPath. Não adicione campos: peers antigos
// falham ao decodificar um erro que carregue um campo desconhecido.
type Error struct {
	ErrorCode      uint16
	HTTPStatusCode *uint16
	WSALastError   *uint16
	TLSAlertCode   *uint8
}

// newError monta um Error com apenas o codigo preenchido.
func newError(code uint16) *Error {
	return &Error{ErrorCode: code}
}

// Pdu é o PDU RDCleanPath completo. Campos opcionais usam ponteiro para
// distinguir "ausente" de "zero".
type Pdu struct {
	Version           uint64
	Error             *Error
	Destination       *string
	ProxyAuth         *string
	ServerAuth        *string
	PreconnectionBlob *string
	X224ConnectionPdu []byte
	ServerCertChain   [][]byte
	ServerAddr        *string
}

// NewRequest monta um request client->proxy (usado nos testes e por clientes).
func NewRequest(x224Pdu []byte, destination, proxyAuth string, pcb *string) *Pdu {
	return &Pdu{
		Version:           Version1,
		Destination:       &destination,
		ProxyAuth:         &proxyAuth,
		PreconnectionBlob: pcb,
		X224ConnectionPdu: x224Pdu,
	}
}

// NewResponse monta um response proxy->client.
func NewResponse(serverAddr string, x224Response []byte, certChain [][]byte) *Pdu {
	return &Pdu{
		Version:           Version1,
		X224ConnectionPdu: x224Response,
		ServerCertChain:   certChain,
		ServerAddr:        &serverAddr,
	}
}

// NewErrorResponse monta um response de erro com HTTP status opcional.
func NewErrorResponse(code uint16, httpStatus *uint16) *Pdu {
	e := &Error{ErrorCode: code, HTTPStatusCode: httpStatus}
	return &Pdu{Version: Version1, Error: e}
}
