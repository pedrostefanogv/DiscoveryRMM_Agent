package remotesession

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	TunnelFrameVersion1   byte = 0x01
	TunnelOpData          byte = 0x01
	TunnelOpClose         byte = 0x02
	TunnelBinaryHeaderLen      = 6
	TunnelJSONLeadingByte byte = '{'
)

// TunnelControlMsg representa mensagens JSON de controle/preflight do tunel multiplexado (.tunnel.in/.tunnel.out).
type TunnelControlMsg struct {
	StreamID   uint32     `json:"streamId"`
	Op         string     `json:"op"` // "open" | "opened" | "data" | "close" | "error" | "preflight"
	TargetHost string     `json:"targetHost,omitempty"`
	TargetPort int        `json:"targetPort,omitempty"`
	Protocol   string     `json:"protocol,omitempty"` // "tcp" | "ssh" | "rdp"
	DataB64    string     `json:"dataB64,omitempty"`
	Error      string     `json:"error,omitempty"`
	Code       string     `json:"code,omitempty"`
	RdpStatus  *RDPStatus `json:"rdpStatus,omitempty"`
}

// EncodeTunnelBinaryHeader escreve o cabecalho binario [0x01][opByte][4B big-endian streamId] em hdr (len >= 6).
func EncodeTunnelBinaryHeader(hdr []byte, op byte, streamID uint32) {
	hdr[0] = TunnelFrameVersion1
	hdr[1] = op
	binary.BigEndian.PutUint32(hdr[2:6], streamID)
}

// EncodeTunnelBinaryData codifica um frame binario DATA [0x01][0x01][4B streamId][payload].
func EncodeTunnelBinaryData(dst []byte, streamID uint32, payload []byte) []byte {
	need := TunnelBinaryHeaderLen + len(payload)
	if cap(dst) < need {
		dst = make([]byte, need)
	} else {
		dst = dst[:need]
	}
	EncodeTunnelBinaryHeader(dst[:TunnelBinaryHeaderLen], TunnelOpData, streamID)
	copy(dst[TunnelBinaryHeaderLen:], payload)
	return dst
}

// EncodeTunnelBinaryClose codifica um frame binario CLOSE [0x01][0x02][4B streamId][optional reason].
func EncodeTunnelBinaryClose(streamID uint32, reason string) []byte {
	buf := make([]byte, TunnelBinaryHeaderLen+len(reason))
	EncodeTunnelBinaryHeader(buf[:TunnelBinaryHeaderLen], TunnelOpClose, streamID)
	if reason != "" {
		copy(buf[TunnelBinaryHeaderLen:], reason)
	}
	return buf
}

// DecodeTunnelFrame decodifica tanto frames binarios (0x01) quanto mensagens de controle JSON (0x7B '{').
func DecodeTunnelFrame(raw []byte) (isBinary bool, opByte byte, streamID uint32, payload []byte, ctrl *TunnelControlMsg, err error) {
	if len(raw) == 0 {
		return false, 0, 0, nil, nil, fmt.Errorf("frame de tunel vazio")
	}

	if raw[0] == TunnelFrameVersion1 {
		if len(raw) < TunnelBinaryHeaderLen {
			return false, 0, 0, nil, nil, fmt.Errorf("frame binario truncado (%d bytes)", len(raw))
		}
		op := raw[1]
		if op != TunnelOpData && op != TunnelOpClose {
			return false, 0, 0, nil, nil, fmt.Errorf("op binario desconhecido: 0x%02x", op)
		}
		sid := binary.BigEndian.Uint32(raw[2:6])
		return true, op, sid, raw[TunnelBinaryHeaderLen:], nil, nil
	}

	trimmed := bytes.TrimLeft(raw, " \t\r\n")
	if len(trimmed) > 0 && trimmed[0] == TunnelJSONLeadingByte {
		var msg TunnelControlMsg
		if uerr := json.Unmarshal(trimmed, &msg); uerr != nil {
			return false, 0, 0, nil, nil, fmt.Errorf("json de controle invalido: %w", uerr)
		}
		msg.Op = strings.ToLower(strings.TrimSpace(msg.Op))
		switch msg.Op {
		case "open", "opened", "data", "close", "error", "preflight":
		default:
			return false, 0, 0, nil, nil, fmt.Errorf("op de controle invalido: %q", msg.Op)
		}
		return false, 0, msg.StreamID, nil, &msg, nil
	}

	return false, 0, 0, nil, nil, fmt.Errorf("prefixo de frame desconhecido: 0x%02x", raw[0])
}
