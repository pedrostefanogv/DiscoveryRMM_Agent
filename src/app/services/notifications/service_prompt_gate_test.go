package notifications

import "testing"

func boolPtr(v bool) *bool { return &v }

// O prompt nativo do PSADT só pode substituir o toast quando a notificação
// continua exigindo confirmação DEPOIS da política por eventType e do rollout.
// Sem esse portão, o Welcome furava enableRequireConfirmation/enableNotifications.
func TestAllowsUserPrompt_RolloutGating(t *testing.T) {
	baseReq := DispatchRequest{
		NotificationID: "n-gate",
		EventType:      "install_start",
		Mode:           "require_confirmation",
	}

	t.Run("sem configuracao bloqueia", func(t *testing.T) {
		s := newTestService()
		if s.AllowsUserPrompt(baseReq) {
			t.Fatalf("sem getAgentConfiguration deveria bloquear")
		}
	})

	t.Run("rollout padrao permite", func(t *testing.T) {
		s := New(Deps{
			Logf: func(string) {},
			GetAgentConfiguration: func() AgentConfiguration {
				return AgentConfiguration{}
			},
		})
		if !s.AllowsUserPrompt(baseReq) {
			t.Fatalf("rollout sem restricoes deveria permitir")
		}
	})

	t.Run("enableRequireConfirmation=false bloqueia", func(t *testing.T) {
		s := New(Deps{
			Logf: func(string) {},
			GetAgentConfiguration: func() AgentConfiguration {
				return AgentConfiguration{Rollout: AgentRolloutConfig{EnableRequireConfirmation: boolPtr(false)}}
			},
		})
		if s.AllowsUserPrompt(baseReq) {
			t.Fatalf("kill switch ligado deveria bloquear o Welcome")
		}
	})

	t.Run("enableNotifications=false bloqueia", func(t *testing.T) {
		s := New(Deps{
			Logf: func(string) {},
			GetAgentConfiguration: func() AgentConfiguration {
				return AgentConfiguration{Rollout: AgentRolloutConfig{EnableNotifications: boolPtr(false)}}
			},
		})
		if s.AllowsUserPrompt(baseReq) {
			t.Fatalf("notificacoes desligadas deveriam bloquear o Welcome")
		}
	})

	t.Run("eventType bloqueado bloqueia", func(t *testing.T) {
		s := New(Deps{
			Logf: func(string) {},
			GetAgentConfiguration: func() AgentConfiguration {
				return AgentConfiguration{Rollout: AgentRolloutConfig{BlockedNotificationEventTypes: []string{"install_start"}}}
			},
		})
		if s.AllowsUserPrompt(baseReq) {
			t.Fatalf("eventType bloqueado deveria bloquear o Welcome")
		}
	})

	t.Run("politica que rebaixa para notify_only bloqueia", func(t *testing.T) {
		s := New(Deps{
			Logf: func(string) {},
			GetAgentConfiguration: func() AgentConfiguration {
				return AgentConfiguration{
					NotificationPolicies: []AgentNotificationPolicy{
						{EventType: "install_start", Mode: "notify_only"},
					},
				}
			},
		})
		if s.AllowsUserPrompt(baseReq) {
			t.Fatalf("politica notify_only deveria bloquear o Welcome")
		}
	})

	t.Run("mode notify_only bloqueia", func(t *testing.T) {
		s := New(Deps{
			Logf: func(string) {},
			GetAgentConfiguration: func() AgentConfiguration {
				return AgentConfiguration{}
			},
		})
		req := baseReq
		req.Mode = "notify_only"
		if s.AllowsUserPrompt(req) {
			t.Fatalf("notify_only nao exige confirmacao")
		}
	})
}
