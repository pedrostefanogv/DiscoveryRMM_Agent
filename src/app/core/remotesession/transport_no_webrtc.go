//go:build !webrtc

package remotesession

// WebRTCAvailable indica se o binario foi compilado com suporte a WebRTC
// (build tag webrtc). Quando false, o transport NATS eh usado como fallback.
const WebRTCAvailable = false
