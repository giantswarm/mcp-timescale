package server

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

const dexProxyChildEnv = "MCP_TIMESCALE_DEX_PROXY_CHILD"

func TestDexDiscoveryFollowsProxyEnvironment(t *testing.T) {
	backend := httptest.NewTLSServer(http.NotFoundHandler())
	defer backend.Close()

	tests := []struct {
		name       string
		noProxy    string
		wantTunnel bool
	}{
		{name: "issuer behind HTTPS_PROXY", wantTunnel: true},
		{name: "issuer in NO_PROXY", noProxy: "example.com", wantTunnel: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			proxy := newTunnelProxy(t, backend.Listener.Addr().String())

			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], //nolint:gosec // re-executes this test binary
				"-test.run=^TestDexDiscoveryProxyChild$", "-test.count=1", "-test.v")
			command.Env = append(environmentWithoutProxy(),
				dexProxyChildEnv+"=1",
				"HTTPS_PROXY="+proxy.server.URL,
				"NO_PROXY="+tc.noProxy,
				"OAUTH_PROVIDER=dex",
				"OAUTH_ISSUER=https://mcp-timescale.example.com",
				"OAUTH_DEX_ISSUER_URL=https://example.com",
				"OAUTH_DEX_CLIENT_ID=mcp-timescale",
				"OAUTH_DEX_CLIENT_SECRET=secret",
				"OAUTH_ALLOW_PRIVATE_URLS=true",
			)
			output, _ := command.CombinedOutput()

			tunnels := proxy.connectedHosts()
			if tc.wantTunnel && (len(tunnels) == 0 || tunnels[0] != "example.com:443") {
				t.Errorf("proxy CONNECT hosts = %v, want example.com:443\nchild output:\n%s", tunnels, output)
			}
			if !tc.wantTunnel && len(tunnels) != 0 {
				t.Errorf("proxy CONNECT hosts = %v, want none for a NO_PROXY issuer\nchild output:\n%s", tunnels, output)
			}
		})
	}
}

// TestDexDiscoveryProxyChild runs only as the child process of
// TestDexDiscoveryFollowsProxyEnvironment: net/http reads the proxy variables
// once per process, so each proxy setting needs a fresh process.
func TestDexDiscoveryProxyChild(t *testing.T) {
	if os.Getenv(dexProxyChildEnv) != "1" {
		t.Skip("child process of TestDexDiscoveryFollowsProxyEnvironment")
	}
	auth, err := NewAuth(t.Context(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil {
		_ = auth.Shutdown(t.Context())
	}
	t.Logf("NewAuth: %v", err)
}

func environmentWithoutProxy() []string {
	var environment []string
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasSuffix(strings.ToUpper(name), "_PROXY") {
			continue
		}
		environment = append(environment, entry)
	}
	return environment
}

// tunnelProxy is an HTTPS proxy that records each CONNECT target and tunnels
// every one of them to a fixed backend address.
type tunnelProxy struct {
	server  *httptest.Server
	mutex   sync.Mutex
	targets []string
}

func newTunnelProxy(t *testing.T, backendAddress string) *tunnelProxy {
	t.Helper()
	proxy := &tunnelProxy{}
	proxy.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT only", http.StatusMethodNotAllowed)
			return
		}
		proxy.mutex.Lock()
		proxy.targets = append(proxy.targets, r.Host)
		proxy.mutex.Unlock()

		backendConnection, err := net.Dial("tcp", backendAddress)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			_ = backendConnection.Close()
			http.Error(w, "hijacking unsupported", http.StatusInternalServerError)
			return
		}
		clientConnection, _, err := hijacker.Hijack()
		if err != nil {
			_ = backendConnection.Close()
			return
		}
		_, _ = clientConnection.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
		go func() {
			_, _ = io.Copy(backendConnection, clientConnection)
			_ = backendConnection.Close()
		}()
		_, _ = io.Copy(clientConnection, backendConnection)
		_ = clientConnection.Close()
	}))
	t.Cleanup(proxy.server.Close)
	return proxy
}

func (proxy *tunnelProxy) connectedHosts() []string {
	proxy.mutex.Lock()
	defer proxy.mutex.Unlock()
	return append([]string(nil), proxy.targets...)
}
