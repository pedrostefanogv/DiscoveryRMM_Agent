//go:build windows

package remotesession

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"discovery/app/core/fileserver"
	"discovery/app/core/testutil"
)

func buildTestDFFrame(t *testing.T, hdr any, payload []byte) []byte {
	t.Helper()
	hb, err := json.Marshal(hdr)
	if err != nil {
		t.Fatalf("marshal DF header: %v", err)
	}
	frame := make([]byte, 6+len(hb)+len(payload))
	frame[0] = 'D'
	frame[1] = 'F'
	binary.BigEndian.PutUint32(frame[2:6], uint32(len(hb)))
	copy(frame[6:6+len(hb)], hb)
	copy(frame[6+len(hb):], payload)
	return frame
}

func TestSessionFiles_BinaryDFRoundTripAndReadyFlags(t *testing.T) {
	srv := startLivenessNATS(t)
	ncAgent, err := nats.Connect(srv.ClientURL(), nats.Timeout(2*time.Second))
	if err != nil {
		t.Fatalf("nats agent: %v", err)
	}
	defer ncAgent.Close()
	ncViewer, err := nats.Connect(srv.ClientURL(), nats.Timeout(2*time.Second))
	if err != nil {
		t.Fatalf("nats viewer: %v", err)
	}
	defer ncViewer.Close()

	const (
		cid = "11111111111111111111111111111111"
		sid = "22222222222222222222222222222222"
		aid = "33333333333333333333333333333333"
		ses = "44444444444444444444444444444444"
	)

	rootDir := testutil.TempDir(t)
	h := NewNatsStreamHandler(ncAgent, cid, sid, aid)
	sf := NewSessionFiles(ses, h, rootDir)

	readySub, err := ncViewer.SubscribeSync("tenant." + cid + ".site." + sid + ".agent." + aid + ".remote.session." + ses + ".files.ready")
	if err != nil {
		t.Fatal(err)
	}
	respSub, err := ncViewer.SubscribeSync("tenant." + cid + ".site." + sid + ".agent." + aid + ".remote.session." + ses + ".files.resp")
	if err != nil {
		t.Fatal(err)
	}
	dataSub, err := ncViewer.SubscribeSync("tenant." + cid + ".site." + sid + ".agent." + aid + ".remote.session." + ses + ".files.data")
	if err != nil {
		t.Fatal(err)
	}
	_ = ncViewer.Flush()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- sf.Start(ctx) }()
	t.Cleanup(func() {
		sf.Stop()
		<-done
	})

	readyMsg, err := readySub.NextMsg(3 * time.Second)
	if err != nil {
		t.Fatalf("files.ready: %v", err)
	}
	var ready map[string]any
	if err := json.Unmarshal(readyMsg.Data, &ready); err != nil {
		t.Fatalf("unmarshal files.ready: %v", err)
	}
	if ready["supportsBinary"] != true || ready["binaryFrames"] != true || ready["dataSubject"] != true {
		t.Fatalf("files.ready missing binary/dataSubject flags: %v", ready)
	}

	reqSubj := "tenant." + cid + ".site." + sid + ".agent." + aid + ".remote.session." + ses + ".files.req"

	// 1. Inbound 'DF' upload (2 chunks)
	blob := bytes.Repeat([]byte{0xDE, 0xAD, 0xBE, 0xEF}, 4096) // 16 KiB
	half := len(blob) / 2
	for idx := 0; idx < 2; idx++ {
		frame := buildTestDFFrame(t, map[string]any{
			"version":     1,
			"requestId":   "put-" + string(rune('0'+idx)),
			"action":      "put",
			"path":        "roundtrip.bin",
			"chunkIndex":  idx,
			"totalChunks": 2,
			"chunkSize":   half,
		}, blob[idx*half:(idx+1)*half])
		if err := ncViewer.Publish(reqSubj, frame); err != nil {
			t.Fatal(err)
		}
		_ = ncViewer.Flush()
		m, err := respSub.NextMsg(3 * time.Second)
		if err != nil {
			t.Fatalf("put resp %d: %v", idx, err)
		}
		var r fileserver.FileSessionResponse
		if err := json.Unmarshal(m.Data, &r); err != nil || !r.Success {
			t.Fatalf("put %d failed: err=%v resp=%s", idx, err, string(m.Data))
		}
	}

	// 2. Outbound 'DF' download via binary=true JSON request -> arrives on .files.data as 'DF'
	for idx := 0; idx < 2; idx++ {
		reqBytes, _ := json.Marshal(map[string]any{
			"version":    1,
			"requestId":  "get-bin-" + string(rune('0'+idx)),
			"action":     "get",
			"path":       "roundtrip.bin",
			"chunkIndex": idx,
			"chunkSize":  half,
			"binary":     true,
		})
		if err := ncViewer.Publish(reqSubj, reqBytes); err != nil {
			t.Fatal(err)
		}
		_ = ncViewer.Flush()
		msg, err := dataSub.NextMsg(3 * time.Second)
		if err != nil {
			t.Fatalf("files.data get %d: %v", idx, err)
		}
		if len(msg.Data) < 6 || msg.Data[0] != 'D' || msg.Data[1] != 'F' {
			t.Fatalf("expected 'DF' frame on .files.data, got %x", msg.Data[:min(6, len(msg.Data))])
		}
		hdrLen := int(binary.BigEndian.Uint32(msg.Data[2:6]))
		var hdr fileserver.FileSessionResponse
		if err := json.Unmarshal(msg.Data[6:6+hdrLen], &hdr); err != nil {
			t.Fatalf("unmarshal DF header: %v", err)
		}
		if !hdr.Success || hdr.Data != nil || hdr.ChunkIndex != idx || hdr.TotalChunks != 2 {
			t.Fatalf("unexpected DF response header: %+v", hdr)
		}
		rawChunk := msg.Data[6+hdrLen:]
		wantChunk := blob[idx*half : (idx+1)*half]
		if !bytes.Equal(rawChunk, wantChunk) {
			t.Fatalf("DF chunk %d payload mismatch: got=%d want=%d", idx, len(rawChunk), len(wantChunk))
		}
	}

	// 3. Legacy JSON get without binary=true -> JSON on .files.resp
	legacyGet, _ := json.Marshal(map[string]any{
		"version":    1,
		"requestId":  "get-json-0",
		"action":     "get",
		"path":       "roundtrip.bin",
		"chunkIndex": 0,
		"chunkSize":  half,
	})
	_ = ncViewer.Publish(reqSubj, legacyGet)
	_ = ncViewer.Flush()
	legMsg, err := respSub.NextMsg(3 * time.Second)
	if err != nil {
		t.Fatalf("legacy files.resp get: %v", err)
	}
	var legResp fileserver.FileSessionResponse
	if err := json.Unmarshal(legMsg.Data, &legResp); err != nil || !legResp.Success || !bytes.Equal(legResp.Data, blob[:half]) {
		t.Fatalf("legacy JSON get failed: err=%v resp=%+v", err, legResp)
	}

	// 4. Stop() closes cached read handle so os.Remove succeeds on Windows
	sf.Stop()
	if err := os.Remove(filepath.Join(rootDir, "roundtrip.bin")); err != nil {
		t.Fatalf("os.Remove after SessionFiles.Stop failed: %v", err)
	}
}
