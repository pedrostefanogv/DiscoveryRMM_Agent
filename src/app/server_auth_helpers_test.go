package app

import (
	"net/http"
	"net/http/httptest"
)

// newConfigServer devolve um servidor que responde /me/configuration com o
// estado de liberação pedido (zeroTouchPending).
func newConfigServer(pending bool) *httptest.Server {
	body := `{"agentId":"8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7","clientId":"c1","siteId":"s1","zeroTouchPending":false}`
	if pending {
		body = `{"agentId":"8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7","clientId":"c1","siteId":"s1","zeroTouchPending":true}`
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
}
