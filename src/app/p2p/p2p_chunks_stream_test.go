package p2p

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildChunkManifest_StreamingHashes(t *testing.T) {
	// 2.5 MiB com chunkSize de 1 MiB -> 3 chunks (1 MiB, 1 MiB, 512 KiB).
	const chunkSize int64 = 1024 * 1024
	const totalSize = 2*1024*1024 + 512*1024

	data := bytes.Repeat([]byte("DiscoveryP2PStreamTest-"), totalSize/23+1)[:totalSize]
	path := filepath.Join(t.TempDir(), "sample-installer.bin")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	manifest, err := buildChunkManifest(context.Background(), path, "art-stream", chunkSize, nil, nil)
	if err != nil {
		t.Fatalf("buildChunkManifest failed: %v", err)
	}
	if manifest.TotalSize != totalSize {
		t.Fatalf("TotalSize = %d, want %d", manifest.TotalSize, totalSize)
	}
	if manifest.TotalChunks != 3 {
		t.Fatalf("TotalChunks = %d, want 3", manifest.TotalChunks)
	}

	fullSum := sha256.Sum256(data)
	wantFull := hex.EncodeToString(fullSum[:])
	if manifest.SHA256 != wantFull {
		t.Fatalf("manifest SHA256 = %s, want %s", manifest.SHA256, wantFull)
	}

	for i, c := range manifest.Chunks {
		chunkSum := sha256.Sum256(data[c.Offset : c.Offset+c.Size])
		wantChunk := hex.EncodeToString(chunkSum[:])
		if c.SHA256 != wantChunk {
			t.Fatalf("chunk %d SHA256 = %s, want %s", i, c.SHA256, wantChunk)
		}
	}
}

func TestIsIgnoredVirtualInterfaceName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"Ethernet", false},
		{"Wi-Fi", false},
		{"vEthernet (WSL (Hyper-V firewall))", true},
		{"vEthernet (Default Switch)", true},
		{"DockerNAT", true},
		{"VMware Network Adapter VMnet8", true},
		{"VirtualBox Host-Only Network", true},
		{"Npcap Loopback Adapter", true},
	}
	for _, tc := range cases {
		if got := isIgnoredVirtualInterfaceName(tc.name); got != tc.want {
			t.Fatalf("isIgnoredVirtualInterfaceName(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}
