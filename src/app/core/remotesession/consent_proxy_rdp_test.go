//go:build windows

package remotesession

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

func TestManager_ProxyAndRdpConsentPromptApproveAndDeny(t *testing.T) {
	srv := startLivenessNATS(t)
	nc, err := nats.Connect(srv.ClientURL(), nats.Timeout(2*time.Second))
	if err != nil {
		t.Fatalf("nats connect: %v", err)
	}
	defer nc.Close()

	for i, kind := range []string{KindProxy, KindRdp} {
		t.Run(kind+"_denied", func(t *testing.T) {
			mgr := NewManager(nc)
			mgr.SetNatsConn(nc, "c1", "s1", "a1")

			var promptCalls, reportCalls atomic.Int32
			mgr.SetInteractiveUser(func() string { return "alice" })
			mgr.SetConsentPrompt(func(_ context.Context, req ConsentRequest) ConsentDecision {
				promptCalls.Add(1)
				return ConsentDecision{
					Accepted:      false,
					Outcome:       "denied",
					Reason:        "user-denied",
					PromptBackend: "test",
					DecidedAt:     time.Now().UTC(),
				}
			})
			mgr.SetConsentReporter(func(_ context.Context, _ string, d ConsentDecision) error {
				reportCalls.Add(1)
				if d.Accepted {
					t.Errorf("expected denied decision")
				}
				return nil
			})

			sid := "deadbeef000" + string(rune('1'+i))
			ok, msg := mgr.HandleCommand(context.Background(), map[string]any{
				"action":    "start",
				"sessionId": sid,
				"kind":      kind,
				"consent": map[string]any{
					"required":       true,
					"mode":           "prompt",
					"timeoutSeconds": 30,
					"timeoutAction":  "deny",
				},
			})
			if !ok || msg == "" {
				t.Fatalf("expected handled=true with refusal reason, got ok=%v msg=%q", ok, msg)
			}
			if promptCalls.Load() != 1 || reportCalls.Load() != 1 {
				t.Fatalf("expected prompt=1 report=1, got prompt=%d report=%d", promptCalls.Load(), reportCalls.Load())
			}
			if mgr.HasSession(sid) || mgr.CountActive() != 0 {
				t.Fatalf("denied %s session should not start (active=%d)", kind, mgr.CountActive())
			}
		})

		t.Run(kind+"_approved", func(t *testing.T) {
			mgr := NewManager(nc)
			mgr.SetNatsConn(nc, "c1", "s1", "a1")
			defer mgr.StopAll()

			var promptCalls, reportCalls atomic.Int32
			mgr.SetInteractiveUser(func() string { return "alice" })
			mgr.SetConsentPrompt(func(_ context.Context, req ConsentRequest) ConsentDecision {
				promptCalls.Add(1)
				return ConsentDecision{
					Accepted:      true,
					Outcome:       "granted",
					Reason:        "user-approved",
					PromptBackend: "test",
					DecidedAt:     time.Now().UTC(),
				}
			})
			mgr.SetConsentReporter(func(_ context.Context, _ string, d ConsentDecision) error {
				reportCalls.Add(1)
				if !d.Accepted {
					t.Errorf("expected approved decision")
				}
				return nil
			})

			sid := "cafe0000000" + string(rune('1'+i))
			ok, msg := mgr.HandleCommand(context.Background(), map[string]any{
				"action":    "start",
				"sessionId": sid,
				"kind":      kind,
				"consent": map[string]any{
					"required":       true,
					"mode":           "prompt",
					"timeoutSeconds": 30,
					"timeoutAction":  "deny",
				},
			})
			if !ok || msg != "" {
				t.Fatalf("expected approved %s start, got ok=%v msg=%q", kind, ok, msg)
			}
			if promptCalls.Load() != 1 || reportCalls.Load() != 1 {
				t.Fatalf("expected prompt=1 report=1, got prompt=%d report=%d", promptCalls.Load(), reportCalls.Load())
			}
			if !mgr.HasSession(sid) {
				t.Fatalf("approved %s session should be active", kind)
			}
		})
	}
}
