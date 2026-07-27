package remotesession

import (
	"fmt"

	"github.com/vmihailenco/msgpack/v5"
)

// EncodeFrameMsgPack codifica um frame em MessagePack em vez de header binario.
// Formato: {seq: uint64, ts: uint64, w: uint16, h: uint16, data: []byte}
// ~40% menor que JSON para frames grandes.
type FrameMsgPack struct {
	Seq    uint64 `msgpack:"s"`
	Ts     uint64 `msgpack:"t"`
	Width  uint16 `msgpack:"w"`
	Height uint16 `msgpack:"h"`
	Data   []byte `msgpack:"d"`
}

// EncodeFrameMsgPack serializa frame + payload JPEG em MessagePack.
func EncodeFrameMsgPack(seq uint64, ts uint64, width, height uint16, data []byte) ([]byte, error) {
	f := FrameMsgPack{
		Seq:    seq,
		Ts:     ts,
		Width:  width,
		Height: height,
		Data:   data,
	}
	return msgpack.Marshal(f)
}

// DecodeFrameMsgPack deserializa um frame MessagePack.
func DecodeFrameMsgPack(raw []byte) (FrameMsgPack, error) {
	var f FrameMsgPack
	if err := msgpack.Unmarshal(raw, &f); err != nil {
		return FrameMsgPack{}, fmt.Errorf("msgpack decode: %w", err)
	}
	return f, nil
}
