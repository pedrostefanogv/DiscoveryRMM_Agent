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

// Portão do toast NATIVO de automation task: o envio sai do processo ANTES do
// Dispatch, então precisa das mesmas regras de política/rollout do Dispatch.
func TestAllowsNotification_RolloutGating(t *testing.T) {
	baseReq := DispatchRequest{
		NotificationID: "n-native",
		EventType:      "install_start",
		Mode:           "notify_only",
	}

	configured := func(cfg AgentConfiguration) *Service {
		return New(Deps{
			Logf:                  func(string) {},
			GetAgentConfiguration: func() AgentConfiguration { return cfg },
		})
	}

	t.Run("sem configuracao bloqueia", func(t *testing.T) {
		if newTestService().AllowsNotification(baseReq) {
			t.Fatal("sem getAgentConfiguration deveria bloquear o toast nativo")
		}
	})

	t.Run("rollout padrao permite notify_only", func(t *testing.T) {
		if !configured(AgentConfiguration{}).AllowsNotification(baseReq) {
			t.Fatal("rollout sem restricoes deveria permitir o toast nativo")
		}
	})

	t.Run("enableNotifications=false bloqueia", func(t *testing.T) {
		s := configured(AgentConfiguration{Rollout: AgentRolloutConfig{EnableNotifications: boolPtr(false)}})
		if s.AllowsNotification(baseReq) {
			t.Fatal("notificacoes desligadas deveriam bloquear o toast nativo")
		}
	})

	t.Run("eventType bloqueado bloqueia", func(t *testing.T) {
		s := configured(AgentConfiguration{Rollout: AgentRolloutConfig{BlockedNotificationEventTypes: []string{"install_start"}}})
		if s.AllowsNotification(baseReq) {
			t.Fatal("eventType bloqueado deveria bloquear o toast nativo")
		}
	})

	t.Run("allowedNotificationEventTypes sem o evento bloqueia", func(t *testing.T) {
		s := configured(AgentConfiguration{Rollout: AgentRolloutConfig{AllowedNotificationEventTypes: []string{"install_end"}}})
		if s.AllowsNotification(baseReq) {
			t.Fatal("allowlist sem install_start deveria bloquear o toast nativo")
		}
	})

	t.Run("politica que rebaixa para silent bloqueia", func(t *testing.T) {
		s := configured(AgentConfiguration{
			NotificationPolicies: []AgentNotificationPolicy{
				{EventType: "install_start", Mode: "silent"},
			},
		})
		if s.AllowsNotification(baseReq) {
			t.Fatal("politica silent deveria bloquear o toast nativo")
		}
	})

	t.Run("politica que exige confirmacao bloqueia o toast informativo", func(t *testing.T) {
		s := configured(AgentConfiguration{
			NotificationPolicies: []AgentNotificationPolicy{
				{EventType: "install_start", Mode: "require_confirmation"},
			},
		})
		if s.AllowsNotification(baseReq) {
			t.Fatal("require_confirmation tem UI propria; toast nativo duplicaria o aviso")
		}
	})
}
