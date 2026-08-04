package p2p

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"discovery/app/p2pmeta"
)

// ComputeFileSHA256 computes the SHA-256 hex digest of a file on disk.
func ComputeFileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ListenInRange opens a TCP listener on the first available port in [start, end].
func ListenInRange(start, end int) (net.Listener, int, error) {
	if start <= 0 || end <= 0 || start > end {
		return nil, 0, errors.New("range de portas invalida")
	}
	for port := start; port <= end; port++ {
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err == nil {
			return ln, port, nil
		}
	}
	return nil, 0, fmt.Errorf("nao foi possivel abrir porta no range %d-%d", start, end)
}

// DetectLocalAddressForPeers returns the local outbound IP address used to
// reach the internet, or "" if it cannot be determined.
func DetectLocalAddressForPeers() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return ""
	}
	defer conn.Close()
	localAddr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || localAddr.IP == nil {
		return ""
	}
	return localAddr.IP.String()
}

// SignReplicationControl signs a replication control payload with HMAC-SHA256.
func SignReplicationControl(secret []byte, sourceAgentID string, access p2pmeta.ArtifactAccess, timestamp string) string {
	payload := strings.Join([]string{
		strings.TrimSpace(sourceAgentID),
		strings.TrimSpace(access.ArtifactName),
		strings.TrimSpace(access.ChecksumSHA256),
		strings.TrimSpace(timestamp),
	}, "\n")
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// ParsePortFromURL extracts the port number from a URL string.
func ParsePortFromURL(raw string) (int, error) {
	parts := strings.Split(strings.TrimSpace(raw), ":")
	if len(parts) < 2 {
		return 0, fmt.Errorf("url sem porta")
	}
	portPart := strings.TrimSpace(parts[len(parts)-1])
	if strings.Contains(portPart, "/") {
		chunks := strings.Split(portPart, "/")
		portPart = chunks[0]
	}
	return strconv.Atoi(portPart)
}

// Aliases lowercase para compatibilidade interna com o código migrado.
var (
	parsePortFromURL           = ParsePortFromURL
	computeFileSHA256          = ComputeFileSHA256
	listenInRange              = ListenInRange
	detectLocalAddressForPeers = DetectLocalAddressForPeers
	signReplicationControl     = SignReplicationControl
)

// buildP2PSeedPlan constrói o plano de seeds a partir da configuração.
func buildP2PSeedPlan(totalAgents int, cfg Config) p2pmeta.SeedPlan {
	return BuildSeedPlan(totalAgents, cfg)
}

// normalizeP2PConfig é um alias para NormalizeConfig.
func normalizeP2PConfig(cfg Config) Config {
	return NormalizeConfig(cfg)
}

// p2pSeedCount é um alias para SeedCount.
func p2pSeedCount(totalAgents, seedPercent, minSeeds int) int {
	return SeedCount(totalAgents, seedPercent, minSeeds)
}

// formatTimeRFC3339 formata um time.Time como RFC3339 UTC, ou "" se zero.
func formatTimeRFC3339(v time.Time) string {
	if v.IsZero() {
		return ""
	}
	return v.UTC().Format(time.RFC3339)
}
