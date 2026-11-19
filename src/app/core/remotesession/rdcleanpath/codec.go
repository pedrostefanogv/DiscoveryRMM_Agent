package rdcleanpath

import (
	"encoding/asn1"
	"fmt"
)

// O PDU RDCleanPath e um SEQUENCE com tag_mode EXPLICIT. Isso significa que
// cada campo e envolvido por uma tag context-specific construida:
//
//   [n] (constructed) { <inner TLV> }
//
// onde o inner TLV e INTEGER, UTF8String, OCTET STRING, ou (para o campo 1 e 7)
// um SEQUENCE. O campo [7] server_cert_chain e `SEQUENCE OF OCTET STRING`
// DENTRO do tag explicito [7]:
//
//   a7 <len> 30 <len> 04 <len> <cert> 04 <len> <cert> ...
//
// Ja o campo [1] error e um SEQUENCE com seus proprios campos context-specific:
//
//   a1 <len> 30 <len> a0 <len> 02 <len> <errorCode> ...

// --- Encoder ---------------------------------------------------------------

// derEncoder acumula TLVs DER em um buffer.
type derEncoder struct{ buf []byte }

// lenBytes codifica o comprimento DER.
func lenBytes(n int) []byte {
	if n < 0x80 {
		return []byte{byte(n)}
	}
	var tmp []byte
	for v := n; v > 0; v >>= 8 {
		tmp = append([]byte{byte(v & 0xff)}, tmp...)
	}
	return append([]byte{0x80 | byte(len(tmp))}, tmp...)
}

// tlv monta um TLV com a tag informada.
func tlv(tag byte, content []byte) []byte {
	out := make([]byte, 0, 1+len(lenBytes(len(content)))+len(content))
	out = append(out, tag)
	out = append(out, lenBytes(len(content))...)
	out = append(out, content...)
	return out
}

// ctxTlv monta um tag context-specific EXPLICIT [n].
func ctxTlv(n int, content []byte) []byte {
	return tlv(byte(0xa0+n), content)
}

// encInteger codifica um INTEGER DER sem sinal (com zero a esquerda se o bit
// mais significativo estiver setado, para nao virar negativo).
func encInteger(v uint64) []byte {
	if v == 0 {
		return tlv(0x02, []byte{0x00})
	}
	var b []byte
	for x := v; x > 0; x >>= 8 {
		b = append([]byte{byte(x & 0xff)}, b...)
	}
	if b[0]&0x80 != 0 {
		b = append([]byte{0x00}, b...)
	}
	return tlv(0x02, b)
}

// encUTF8String codifica um UTF8String DER.
func encUTF8String(s string) []byte {
	return tlv(0x0c, []byte(s))
}

// encOctetString codifica um OCTET STRING DER.
func encOctetString(b []byte) []byte {
	return tlv(0x04, b)
}

// Encode serializa o PDU para DER.
func (p *Pdu) Encode() ([]byte, error) {
	if p == nil {
		return nil, fmt.Errorf("rdcleanpath: pdu nil")
	}
	var body []byte

	body = append(body, ctxTlv(tagVersion, encInteger(p.Version))...)

	if p.Error != nil {
		body = append(body, ctxTlv(tagError, encodeError(p.Error))...)
	}
	if p.Destination != nil {
		body = append(body, ctxTlv(tagDestination, encUTF8String(*p.Destination))...)
	}
	if p.ProxyAuth != nil {
		body = append(body, ctxTlv(tagProxyAuth, encUTF8String(*p.ProxyAuth))...)
	}
	if p.ServerAuth != nil {
		body = append(body, ctxTlv(tagServerAuth, encUTF8String(*p.ServerAuth))...)
	}
	if p.PreconnectionBlob != nil {
		body = append(body, ctxTlv(tagPreconnectionBlob, encUTF8String(*p.PreconnectionBlob))...)
	}
	if p.X224ConnectionPdu != nil {
		body = append(body, ctxTlv(tagX224ConnectionPdu, encOctetString(p.X224ConnectionPdu))...)
	}
	if p.ServerCertChain != nil {
		// SEQUENCE OF OCTET STRING dentro do tag explicito.
		var seq []byte
		for _, cert := range p.ServerCertChain {
			seq = append(seq, encOctetString(cert)...)
		}
		body = append(body, ctxTlv(tagServerCertChain, tlv(0x30, seq))...)
	}
	if p.ServerAddr != nil {
		body = append(body, ctxTlv(tagServerAddr, encUTF8String(*p.ServerAddr))...)
	}

	return tlv(0x30, body), nil
}

// encodeError monta o SEQUENCE do RDCleanPathErr.
func encodeError(e *Error) []byte {
	var body []byte
	body = append(body, ctxTlv(errTagErrorCode, encInteger(uint64(e.ErrorCode)))...)
	if e.HTTPStatusCode != nil {
		body = append(body, ctxTlv(errTagHTTPStatus, encInteger(uint64(*e.HTTPStatusCode)))...)
	}
	if e.WSALastError != nil {
		body = append(body, ctxTlv(errTagWSALastError, encInteger(uint64(*e.WSALastError)))...)
	}
	if e.TLSAlertCode != nil {
		body = append(body, ctxTlv(errTagTLSAlertCode, encInteger(uint64(*e.TLSAlertCode)))...)
	}
	return tlv(0x30, body)
}

// --- Decoder ---------------------------------------------------------------

// asn1Unmarshal é o ponto de entrada do decoder. Usamos encoding/asn1 com
// struct tags explicit para espelhar a definicao Rust.
func unmarshalDER(der []byte) (*Pdu, error) {
	var raw rawPdu
	rest, err := asn1.Unmarshal(der, &raw)
	if err != nil {
		return nil, fmt.Errorf("rdcleanpath: DER invalido: %w", err)
	}
	if len(rest) != 0 {
		return nil, fmt.Errorf("rdcleanpath: %d bytes sobrando apos o PDU", len(rest))
	}
	return raw.toPdu()
}

// rawPdu espelha RDCleanPathPdu com tag_mode EXPLICIT. Todos os campos sao
// opcionais e context-specific.
type rawPdu struct {
	// encoding/asn1 nao suporta uint64; usamos int64 e validamos o valor.
	Version int64 `asn1:"explicit,tag:0"`
	// encoding/asn1 nao aceita ponteiro para struct aqui; usamos valor e
	// detectamos presenca por ErrorCode != 0 (0 e reservado).
	Error             rawError        `asn1:"explicit,optional,tag:1"`
	Destination       string          `asn1:"explicit,optional,tag:2,utf8"`
	ProxyAuth         string          `asn1:"explicit,optional,tag:3,utf8"`
	ServerAuth        string          `asn1:"explicit,optional,tag:4,utf8"`
	PreconnectionBlob string          `asn1:"explicit,optional,tag:5,utf8"`
	X224ConnectionPdu []byte          `asn1:"explicit,optional,tag:6"`
	ServerCertChain   []asn1.RawValue `asn1:"explicit,optional,tag:7"`
	ServerAddr        string          `asn1:"explicit,optional,tag:9,utf8"`
}

// rawError espelha RDCleanPathErr. Os campos opcionais sao decodificados como
// RawValue porque encoding/asn1 nao aceita *int dentro de tag explicito.
type rawError struct {
	ErrorCode      int           `asn1:"explicit,tag:0"`
	HTTPStatusCode asn1.RawValue `asn1:"explicit,optional,tag:1"`
	WSALastError   asn1.RawValue `asn1:"explicit,optional,tag:2"`
	TLSAlertCode   asn1.RawValue `asn1:"explicit,optional,tag:3"`
}

// optInt extrai um inteiro opcional de um RawValue (ausente => nil).
//
// Com `explicit,optional`, o RawValue representa o wrapper [n]; o inteiro esta
// no TLV interno, acessivel por FullBytes (que inclui o wrapper) ou por Bytes
// (conteudo do wrapper, ou seja o TLV interno completo).
func optInt(rv asn1.RawValue) (*int, error) {
	if len(rv.Bytes) == 0 {
		return nil, nil
	}
	var v int
	if _, err := asn1.Unmarshal(rv.Bytes, &v); err != nil {
		// Alguns caminhos do asn1 preenchem apenas FullBytes.
		if _, err2 := asn1.Unmarshal(rv.FullBytes, &v); err2 != nil {
			return nil, fmt.Errorf("rdcleanpath: campo inteiro opcional invalido: %w", err)
		}
	}
	return &v, nil
}

// toU16 valida e converte um int de erro para uint16.
func toU16(v int) (uint16, error) {
	if v < 0 || v > 0xffff {
		return 0, fmt.Errorf("rdcleanpath: valor %d fora do range uint16", v)
	}
	return uint16(v), nil
}

func (r *rawPdu) toPdu() (*Pdu, error) {
	if r.Version < 0 || uint64(r.Version) != Version1 {
		return nil, fmt.Errorf("rdcleanpath: versao nao suportada %d (esperado %d)", r.Version, Version1)
	}
	p := &Pdu{
		Version:           uint64(r.Version),
		X224ConnectionPdu: r.X224ConnectionPdu,
	}
	// ErrorCode 0 e reservado, entao != 0 significa que [1] veio no PDU.
	if r.Error.ErrorCode != 0 {
		code, err := toU16(r.Error.ErrorCode)
		if err != nil {
			return nil, err
		}
		e := &Error{ErrorCode: code}
		if e.HTTPStatusCode, err = optIntU16(r.Error.HTTPStatusCode); err != nil {
			return nil, err
		}
		if e.WSALastError, err = optIntU16(r.Error.WSALastError); err != nil {
			return nil, err
		}
		if v, verr := optInt(r.Error.TLSAlertCode); verr != nil {
			return nil, verr
		} else if v != nil {
			u, uerr := toU16(*v)
			if uerr != nil {
				return nil, uerr
			}
			b := uint8(u)
			e.TLSAlertCode = &b
		}
		p.Error = e
	}
	if r.Destination != "" {
		d := r.Destination
		p.Destination = &d
	}
	if r.ProxyAuth != "" {
		a := r.ProxyAuth
		p.ProxyAuth = &a
	}
	if r.ServerAuth != "" {
		a := r.ServerAuth
		p.ServerAuth = &a
	}
	if r.PreconnectionBlob != "" {
		b := r.PreconnectionBlob
		p.PreconnectionBlob = &b
	}
	if r.ServerAddr != "" {
		a := r.ServerAddr
		p.ServerAddr = &a
	}
	if r.ServerCertChain != nil {
		certs := make([][]byte, 0, len(r.ServerCertChain))
		for _, rv := range r.ServerCertChain {
			if rv.Tag != asn1.TagOctetString {
				return nil, fmt.Errorf("rdcleanpath: server_cert_chain esperava OCTET STRING, veio tag %d", rv.Tag)
			}
			certs = append(certs, rv.Bytes)
		}
		p.ServerCertChain = certs
	}
	return p, nil
}

// optIntU16 le um inteiro opcional e o converte para *uint16.
func optIntU16(rv asn1.RawValue) (*uint16, error) {
	v, err := optInt(rv)
	if err != nil || v == nil {
		return nil, err
	}
	u, err := toU16(*v)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// Decode faz o parse de um PDU RDCleanPath a partir de DER.
func Decode(der []byte) (*Pdu, error) {
	if len(der) == 0 {
		return nil, fmt.Errorf("rdcleanpath: buffer vazio")
	}
	return unmarshalDER(der)
}
