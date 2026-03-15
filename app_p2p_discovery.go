package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/grandcat/zeroconf"
)

const (
	p2pMDNSService = "_discovery-p2p._tcp"
	p2pMDNSDomain  = "local."
	p2pUDPPort     = 41079
)

type p2pSelfEndpoint struct {
	AgentID string
	Host    string
	Port    int
}

type p2pDiscoveredPeer struct {
	AgentID      string
	Host         string
	Address      string
	Port         int
	Source       string
	KnownPeers   int
	ConnectedVia string
}

type p2pDiscoveryProvider interface {
	Name() string
	Start(ctx context.Context, self p2pSelfEndpoint, onPeer func(peer p2pDiscoveredPeer)) error
}

type p2pMDNSProvider struct{}

func (p *p2pMDNSProvider) Name() string {
	return p2pDiscoveryMDNS
}

func (p *p2pMDNSProvider) Start(ctx context.Context, self p2pSelfEndpoint, onPeer func(peer p2pDiscoveredPeer)) error {
	instance := self.AgentID
	if strings.TrimSpace(instance) == "" {
		hostname, _ := os.Hostname()
		instance = strings.TrimSpace(hostname)
		if instance == "" {
			instance = fmt.Sprintf("discovery-%d", time.Now().Unix())
		}
	}
	instance = sanitizeMDNSInstance(instance)

	txt := []string{
		"agentId=" + strings.TrimSpace(self.AgentID),
		"transport=http",
		"connectedVia=mdns",
	}

	server, err := zeroconf.Register(instance, p2pMDNSService, p2pMDNSDomain, self.Port, txt, nil)
	if err != nil {
		return err
	}

	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		server.Shutdown()
		return err
	}

	entries := make(chan *zeroconf.ServiceEntry)
	go func() {
		<-ctx.Done()
		server.Shutdown()
	}()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case entry := <-entries:
				if entry == nil {
					continue
				}
				peer := parseMDNSEntry(entry)
				if peer.AgentID == "" {
					continue
				}
				if strings.EqualFold(strings.TrimSpace(peer.AgentID), strings.TrimSpace(self.AgentID)) {
					continue
				}
				onPeer(peer)
			}
		}
	}()

	browseCtx, browseCancel := context.WithCancel(ctx)
	go func() {
		<-ctx.Done()
		browseCancel()
	}()
	if err := resolver.Browse(browseCtx, p2pMDNSService, p2pMDNSDomain, entries); err != nil {
		server.Shutdown()
		browseCancel()
		return err
	}

	return nil
}

type p2pUDPProvider struct{}

type p2pUDPAnnouncement struct {
	AgentID    string `json:"agentId"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	KnownPeers int    `json:"knownPeers"`
	Source     string `json:"source"`
}

func (p *p2pUDPProvider) Name() string {
	return p2pDiscoveryUDP
}

func (p *p2pUDPProvider) Start(ctx context.Context, self p2pSelfEndpoint, onPeer func(peer p2pDiscoveredPeer)) error {
	addr := &net.UDPAddr{IP: net.IPv4zero, Port: p2pUDPPort}
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		return err
	}
	broadcastAddr := &net.UDPAddr{IP: net.IPv4bcast, Port: p2pUDPPort}

	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	go func() {
		buffer := make([]byte, 2048)
		for {
			n, remote, err := conn.ReadFromUDP(buffer)
			if err != nil {
				select {
				case <-ctx.Done():
					return
				default:
					continue
				}
			}
			var msg p2pUDPAnnouncement
			if err := json.Unmarshal(buffer[:n], &msg); err != nil {
				continue
			}
			if strings.EqualFold(strings.TrimSpace(msg.AgentID), strings.TrimSpace(self.AgentID)) {
				continue
			}
			peerAddress := ""
			if remote != nil && remote.IP != nil {
				peerAddress = remote.IP.String()
			}
			onPeer(p2pDiscoveredPeer{
				AgentID:      strings.TrimSpace(msg.AgentID),
				Host:         strings.TrimSpace(msg.Host),
				Address:      peerAddress,
				Port:         msg.Port,
				Source:       p2pDiscoveryUDP,
				KnownPeers:   msg.KnownPeers,
				ConnectedVia: p2pDiscoveryUDP,
			})
		}
	}()

	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		announce := func() {
			payload, err := json.Marshal(p2pUDPAnnouncement{
				AgentID: strings.TrimSpace(self.AgentID),
				Host:    strings.TrimSpace(self.Host),
				Port:    self.Port,
				Source:  p2pDiscoveryUDP,
			})
			if err == nil {
				_, _ = conn.WriteToUDP(payload, broadcastAddr)
			}
		}
		announce()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				announce()
			}
		}
	}()

	return nil
}

func parseMDNSEntry(entry *zeroconf.ServiceEntry) p2pDiscoveredPeer {
	txt := parseTXT(entry.Text)
	agentID := strings.TrimSpace(txt["agentid"])
	address := ""
	if len(entry.AddrIPv4) > 0 {
		address = entry.AddrIPv4[0].String()
	} else if len(entry.AddrIPv6) > 0 {
		address = entry.AddrIPv6[0].String()
	}

	knownPeers, _ := strconv.Atoi(strings.TrimSpace(txt["knownpeers"]))
	connectedVia := strings.TrimSpace(txt["connectedvia"])
	if connectedVia == "" {
		connectedVia = p2pDiscoveryMDNS
	}

	return p2pDiscoveredPeer{
		AgentID:      agentID,
		Host:         strings.TrimSpace(entry.HostName),
		Address:      address,
		Port:         entry.Port,
		Source:       p2pDiscoveryMDNS,
		KnownPeers:   knownPeers,
		ConnectedVia: connectedVia,
	}
}

func parseTXT(txt []string) map[string]string {
	out := make(map[string]string, len(txt))
	for _, item := range txt {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		parts := strings.SplitN(item, "=", 2)
		key := strings.ToLower(strings.TrimSpace(parts[0]))
		if key == "" {
			continue
		}
		if len(parts) == 1 {
			out[key] = ""
			continue
		}
		out[key] = strings.TrimSpace(parts[1])
	}
	return out
}

func sanitizeMDNSInstance(value string) string {
	v := strings.TrimSpace(value)
	if v == "" {
		return "discovery-agent"
	}
	v = strings.ReplaceAll(v, " ", "-")
	if len(v) > 63 {
		v = v[:63]
	}
	return v
}
