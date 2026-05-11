package main

import (
"encoding/json"
"fmt"
"os"
"strings"
"time"

"discovery/app/netutil"
"github.com/nats-io/nats.go"
)

type localConfig struct {
APIServer string `json:"apiServer"`
AuthToken string `json:"authToken"`
}

type remoteDebugLogMessage struct {
SessionID    string `json:"sessionId"`
Sequence     uint64 `json:"sequence"`
TimestampUTC string `json:"timestampUtc"`
Level        string `json:"level"`
Message      string `json:"message"`
Logger       string `json:"logger,omitempty"`
Category     string `json:"category,omitempty"`
}

func main() {
subject := "tenant.019dead3-70ee-7c66-8662-7558e0b23ad5.site.019dead3-845a-79e3-9769-f94a0f6a114f.agent.019e0049-ec79-7def-9452-91f69e044acb.remote-debug.log"
if len(os.Args) > 1 && strings.TrimSpace(os.Args[1]) != "" {
subject = strings.TrimSpace(os.Args[1])
}

raw, err := os.ReadFile(`C:\ProgramData\Discovery\config.json`)
if err != nil {
fmt.Println("ERROR_READ_CONFIG:", err)
os.Exit(1)
}

var cfg localConfig
if err := json.Unmarshal(raw, &cfg); err != nil {
fmt.Println("ERROR_PARSE_CONFIG:", err)
os.Exit(1)
}
if strings.TrimSpace(cfg.APIServer) == "" {
fmt.Println("ERROR_CONFIG: apiServer vazio")
os.Exit(1)
}
if strings.TrimSpace(cfg.AuthToken) == "" {
fmt.Println("ERROR_CONFIG: authToken vazio")
os.Exit(1)
}

normalizedToken, err := netutil.NormalizeAgentToken(cfg.AuthToken)
if err != nil {
fmt.Println("ERROR_TOKEN:", err)
os.Exit(1)
}

host := strings.TrimSpace(cfg.APIServer); if strings.Contains(host, ":") { host = strings.Split(host, ":")[0] }; server := "nats://" + host + ":4222"
nc, err := nats.Connect(server,
nats.Name("discovery-remote-debug-manual-probe"),
nats.Token(normalizedToken),
nats.Timeout(5*time.Second),
nats.ReconnectWait(2*time.Second),
nats.MaxReconnects(1),
)
if err != nil {
fmt.Println("ERROR_CONNECT:", err)
os.Exit(1)
}
defer nc.Close()

sub, err := nc.SubscribeSync(subject)
if err != nil {
fmt.Println("ERROR_SUBSCRIBE:", err)
os.Exit(1)
}
nc.Flush()
if err := nc.LastError(); err != nil {
fmt.Println("ERROR_SUBSCRIBE_FLUSH:", err)
os.Exit(1)
}

sessionID := "manual-" + time.Now().UTC().Format("20060102T150405Z")
waitInboundUntil := time.Now().Add(3 * time.Second)
for time.Now().Before(waitInboundUntil) {
msg, err := sub.NextMsg(400 * time.Millisecond)
if err != nil {
continue
}
var inbound remoteDebugLogMessage
if json.Unmarshal(msg.Data, &inbound) == nil && strings.TrimSpace(inbound.SessionID) != "" {
sessionID = strings.TrimSpace(inbound.SessionID)
fmt.Printf("SAMPLE_INBOUND sessionId=%s level=%s\n", sessionID, strings.TrimSpace(inbound.Level))
break
}
}

marker := fmt.Sprintf("COPILOT_REMOTE_DEBUG_TEST_%d", time.Now().Unix())
out := remoteDebugLogMessage{
SessionID:    sessionID,
Sequence:     uint64(time.Now().UnixNano()),
TimestampUTC: time.Now().UTC().Format(time.RFC3339Nano),
Level:        "info",
Message:      marker,
Logger:       "manual-probe",
Category:     "manual-test",
}
payload, err := json.Marshal(out)
if err != nil {
fmt.Println("ERROR_MARSHAL:", err)
os.Exit(1)
}

if err := nc.Publish(subject, payload); err != nil {
fmt.Println("ERROR_PUBLISH:", err)
os.Exit(1)
}
nc.Flush()
if err := nc.LastError(); err != nil {
fmt.Println("ERROR_PUBLISH_FLUSH:", err)
os.Exit(1)
}
fmt.Printf("PUBLISHED subject=%s sessionId=%s marker=%s\n", subject, sessionID, marker)

confirmUntil := time.Now().Add(8 * time.Second)
for time.Now().Before(confirmUntil) {
msg, err := sub.NextMsg(700 * time.Millisecond)
if err != nil {
continue
}
var inbound remoteDebugLogMessage
if json.Unmarshal(msg.Data, &inbound) != nil {
continue
}
if strings.Contains(inbound.Message, marker) {
fmt.Printf("CONFIRMED_RECEIVE marker=%s level=%s timestampUtc=%s\n", marker, inbound.Level, inbound.TimestampUTC)
return
}
}

fmt.Println("NOT_CONFIRMED: mensagem publicada mas sem eco confirmado na janela de espera")
os.Exit(2)
}


