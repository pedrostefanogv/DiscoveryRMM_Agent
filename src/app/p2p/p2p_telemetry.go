package p2p

import (
	"math"
	"runtime"
	"strings"
)

// collectHostLoad retorna um snapshot da carga atual do host para telemetria enriquecida.
// Reusa a coleta de métricas já existente do sistema de heartbeat (getHeartbeatMetrics).
func (c *Coordinator) CollectHostLoad() P2PHostLoad {
	load := P2PHostLoad{
		CPUCores: runtime.NumCPU(),
	}

	// Reusa as métricas do heartbeat que já são coletadas via osquery.
	if c.deps != nil {
		metrics := c.deps.GetHeartbeatMetrics()
		if metrics.CpuPercent >= 0 {
			load.CPUPercent = metrics.CpuPercent
		}
		if metrics.MemoryPercent >= 0 {
			load.MemoryPercent = metrics.MemoryPercent
		}
		if metrics.MemoryTotalGb > 0 {
			load.RamGB = metrics.MemoryTotalGb
		}
		// DiskBusyPercent aproximado pela média de read/write percent
		if metrics.DiskReadPercent >= 0 || metrics.DiskWritePercent >= 0 {
			total := math.Max(metrics.DiskReadPercent, 0) + math.Max(metrics.DiskWritePercent, 0)
			if total > 100 {
				total = 100
			}
			load.DiskBusyPercent = math.Min(total, 100.0)
		}
	}

	return load
}

// countKnownPeers retorna o total de peers no registry do coordinator.
func (c *Coordinator) CountKnownPeers() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.knownPeers
}

// countConnectedLibp2pPeers retorna a quantidade de peers conectados via libp2p.
func (c *Coordinator) CountConnectedLibp2pPeers() int {
	h, _ := c.libp2pHostAndRegistry()
	if h == nil {
		return 0
	}
	return len(h.Network().Peers())
}

// getP2PAddressingInfo retorna o peerID, addrs roteáveis e porta libp2p para enriquecer heartbeats.
func (c *Coordinator) GetP2PAddressingInfo() (peerID string, addrs []string, port int) {
	h, _ := c.libp2pHostAndRegistry()
	if h == nil {
		return "", nil, 0
	}
	peerID = h.ID().String()
	addrs = extractRoutableIPv4Addrs(h)

	// Extrair a porta libp2p dos multiaddrs de escuta.
	for _, addr := range h.Addrs() {
		s := addr.String()
		// Procura por /tcp/{port}/ ou /udp/{port}/
		parts := strings.Split(s, "/")
		for i, part := range parts {
			if (part == "tcp" || part == "udp") && i+1 < len(parts) {
				if p, err := parsePortFromURL(":" + parts[i+1]); err == nil && p > 0 {
					port = p
					return peerID, addrs, port
				}
			}
		}
	}

	return peerID, addrs, 0
}
