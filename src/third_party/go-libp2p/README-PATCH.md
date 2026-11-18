# Fork local do go-libp2p v0.51.0 — sem transporte WebRTC

Fork local do módulo github.com/libp2p/go-libp2p v0.51.0 com uma única
alteração: remoção do transporte WebRTC (pion/webrtc/v4) dos transportes
padrão e do grafo de DI do config.NewNode.

Por quê: o Agent usa libp2p apenas com TCP + QUIC (+ mDNS para descoberta
LAN). O transporte WebRTC vinha embutido via defaults.go/config.go da raiz,
arrastando pion/webrtc/v4 para o binário sem nunca ser usado.

Patch aplicado (upstream -> fork):
1. defaults.go: removidos o import libp2pwebrtc, a entrada
   Transport(libp2pwebrtc.New) de DefaultTransports e os listen addrs
   webrtc-direct de DefaultListenAddrs.
2. config/config.go: removidos o import libp2pwebrtc e o bloco
   fx.Provide(... libp2pwebrtc.ListenUDPFn ...).
3. Excluídos p2p/transport/webrtc/ e p2p/net/swarm/testing/ (referenciam
   pion diretamente; não são importados pelo agente) e todos os *_test.go.

Nenhuma outra linha foi alterada. identify, holepunch, relay, QUIC, TCP,
noise/TLS e yamux permanecem intactos.

Manutenção: ao atualizar o libp2p, regenerar este fork a partir da nova
versão e reaplicar os 3 pontos acima. Verificação:
grep -ri webrtc third_party/go-libp2p --include="*.go" deve retornar apenas
comentários/constantes multiaddr (ma.P_WEBRTC*), nenhum import de pion/webrtc.
