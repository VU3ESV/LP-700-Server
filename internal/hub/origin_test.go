package hub

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// A browser on the operator's machine must not be able to drive the meter
// from another site's page (cross-site WebSocket hijacking); the hub's own
// web UI, a TLS proxy in front of it, and Origin-less native clients must.
func TestServeWSOriginPolicy(t *testing.T) {
	h := NewHub(nil, nil, nil, nil, Options{Heartbeat: time.Second, MaxClients: 4},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv := httptest.NewServer(http.HandlerFunc(h.ServeWS))
	defer srv.Close()

	hostport := strings.TrimPrefix(srv.URL, "http://")
	host := strings.Split(hostport, ":")[0]
	wsURL := "ws://" + hostport + "/ws"

	cases := []struct {
		name, origin string
		want         int
	}{
		{"no Origin (native app, Node-RED)", "", http.StatusSwitchingProtocols},
		{"same origin (embedded web UI)", "http://" + hostport, http.StatusSwitchingProtocols},
		{"same origin through a TLS proxy", "https://" + hostport, http.StatusSwitchingProtocols},
		{"another site", "https://evil.example", http.StatusForbidden},
		{"same host, other port (e.g. :1880)", "http://" + host + ":1880", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hdr := http.Header{}
			if tc.origin != "" {
				hdr.Set("Origin", tc.origin)
			}
			conn, resp, err := websocket.DefaultDialer.Dial(wsURL, hdr)
			if conn != nil {
				conn.Close()
			}
			if resp == nil {
				t.Fatalf("no HTTP response: %v", err)
			}
			if resp.StatusCode != tc.want {
				t.Fatalf("Origin %q: status %d, want %d", tc.origin, resp.StatusCode, tc.want)
			}
		})
	}
}
