//go:build webrtc
// +build webrtc

package remotesession

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/pion/webrtc/v4"
)

// WebRTCSession gerencia uma conexao WebRTC P2P com o browser.
type WebRTCSession struct {
	sessionID     string
	peerConn      *webrtc.PeerConnection
	videoTrack    *webrtc.TrackLocalStaticSample
	natsStream    *NatsStreamHandler
	stunURLs      []string
	turnURLs      []string
	turnUsername  string
	turnCredential string
}

// NewWebRTCSession cria uma nova sessao WebRTC.
func NewWebRTCSession(
	sessionID string,
	natsStream *NatsStreamHandler,
	stunURLs []string,
	turnURLs []string,
	turnUsername string,
	turnCredential string,
) (*WebRTCSession, error) {
	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{},
	}

	if len(stunURLs) > 0 {
		config.ICEServers = append(config.ICEServers, webrtc.ICEServer{URLs: stunURLs})
	}
	if len(turnURLs) > 0 {
		config.ICEServers = append(config.ICEServers, webrtc.ICEServer{
			URLs:       turnURLs,
			Username:   turnUsername,
			Credential: turnCredential,
		})
	}

	// Cria video track
	videoTrack, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8},
		"screen", "discovery-rmm",
	)
	if err != nil {
		return nil, fmt.Errorf("webrtc track: %w", err)
	}

	peerConn, err := webrtc.NewPeerConnection(config)
	if err != nil {
		return nil, fmt.Errorf("webrtc peer connection: %w", err)
	}

	if _, err = peerConn.AddTrack(videoTrack); err != nil {
		peerConn.Close()
		return nil, fmt.Errorf("webrtc add track: %w", err)
	}

	return &WebRTCSession{
		sessionID:     sessionID,
		peerConn:      peerConn,
		videoTrack:    videoTrack,
		natsStream:    natsStream,
		stunURLs:      stunURLs,
		turnURLs:      turnURLs,
		turnUsername:  turnUsername,
		turnCredential: turnCredential,
	}, nil
}

// Start inicia a negociacao WebRTC e aguarda a conexao.
// O browser envia offer via NATS signal; o agent responde com answer.
func (w *WebRTCSession) Start(ctx context.Context) error {
	// Subscreve a signaling
	sub, err := w.natsStream.SubscribeToSignal(w.sessionID, func(data []byte) {
		w.handleSignal(data)
	})
	if err != nil {
		return fmt.Errorf("webrtc signal subscribe: %w", err)
	}
	defer sub.Unsubscribe()

	// Aguarda conexao ou timeout
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(30 * time.Second):
		return fmt.Errorf("webrtc ice timeout")
	}
}

func (w *WebRTCSession) handleSignal(data []byte) {
	var signal map[string]any
	if err := json.Unmarshal(data, &signal); err != nil {
		return
	}

	signalType, _ := signal["type"].(string)
	switch signalType {
	case "offer":
		w.handleOffer(data)
	case "candidate":
		w.handleICECandidate(data)
	}
}

func (w *WebRTCSession) handleOffer(data []byte) {
	var sdp webrtc.SessionDescription
	if err := json.Unmarshal(data, &sdp); err != nil {
		return
	}
	w.peerConn.SetRemoteDescription(sdp)

	answer, err := w.peerConn.CreateAnswer(nil)
	if err != nil {
		return
	}
	w.peerConn.SetLocalDescription(answer)

	// Envia answer via NATS
	answerJSON, err := json.Marshal(answer)
	if err != nil {
		return
	}
	w.natsStream.PublishSignal(w.sessionID, answerJSON)
}

func (w *WebRTCSession) handleICECandidate(data []byte) {
	var candidate webrtc.ICECandidateInit
	if err := json.Unmarshal(data, &candidate); err != nil {
		return
	}
	w.peerConn.AddICECandidate(candidate)
}

// Close encerra a conexao WebRTC.
func (w *WebRTCSession) Close() error {
	return w.peerConn.Close()
}

// Ensure imports
var _ webrtc.MediaEngine
var _ nats.Conn
var _ = context.Background
var _ = fmt.Println
