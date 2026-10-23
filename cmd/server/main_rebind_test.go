package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func pingHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("pong"))
	})
	return mux
}

func freeAddr(t *testing.T) (string, int) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := l.Addr().String()
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}
	l.Close()
	return addr, port
}

func writeSelfSignedTLS(t *testing.T) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	keyBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes}), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return certPath, keyPath
}

func plainClient() *http.Client {
	return &http.Client{Timeout: 2 * time.Second}
}

func insecureTLSClient() *http.Client {
	return &http.Client{
		Timeout:   2 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}
}

func waitServing(t *testing.T, client *http.Client, url string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
			t.Fatalf("%s: status %d, want 200", url, resp.StatusCode)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s: not serving after 10s", url)
}

// wireRebinder builds a rebinder whose CURRENT listener is already live
// (a real Serve on addr), matching how main() wires it at startup.
func wireRebinder(t *testing.T, addr string, port int) (*liveRebinder, *http.Server) {
	t.Helper()
	srv := &http.Server{Addr: addr, Handler: pingHandler()}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("initial listen on %s: %v", addr, err)
	}
	go srv.Serve(ln)
	reb := newLiveRebinder()
	reb.server = &srv
	reb.router = srv.Handler
	reb.curAddr = addr
	reb.curPort = port
	return reb, srv
}

// TestRebindSameAddrEnableTLS reproduces the 0.1.6 self-test failure:
// switching tls_cert/tls_key ON without changing host/port returned
// rebind_status "failed" ("listen tcp: Only one usage of each socket
// address") because RebindWithTLS bound the new listener BEFORE shutting
// the old one down — impossible on the same addr:port.
func TestRebindSameAddrEnableTLS(t *testing.T) {
	addr, port := freeAddr(t)
	certPath, keyPath := writeSelfSignedTLS(t)
	reb, _ := wireRebinder(t, addr, port)
	waitServing(t, plainClient(), "http://"+addr+"/ping")

	if err := reb.RebindWithTLS(addr, certPath, keyPath); err != nil {
		t.Fatalf("same-addr TLS rebind failed: %v", err)
	}

	tlsURL := "https://" + addr + "/ping"
	waitServing(t, insecureTLSClient(), tlsURL)

	// The new listener must be HTTPS-only: plaintext must no longer reach
	// the handler (Go may close the connection or answer 400).
	if resp, err := plainClient().Get("http://" + addr + "/ping"); err == nil {
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("plaintext GET reached the handler after enabling TLS (status 200)")
		}
	}
	if reb.curAddr != addr {
		t.Fatalf("curAddr = %q, want %q", reb.curAddr, addr)
	}
}

// TestRebindSameAddrDisableTLS covers the reverse toggle (HTTPS -> HTTP)
// on the same addr:port, which is blocked by the same bind-before-shutdown
// flaw.
func TestRebindSameAddrDisableTLS(t *testing.T) {
	addr, port := freeAddr(t)
	certPath, keyPath := writeSelfSignedTLS(t)
	reb, _ := wireRebinder(t, addr, port)
	waitServing(t, plainClient(), "http://"+addr+"/ping")

	if err := reb.RebindWithTLS(addr, certPath, keyPath); err != nil {
		t.Fatalf("same-addr TLS enable failed: %v", err)
	}
	waitServing(t, insecureTLSClient(), "https://"+addr+"/ping")

	if err := reb.RebindWithTLS(addr, "", ""); err != nil {
		t.Fatalf("same-addr TLS disable failed: %v", err)
	}
	waitServing(t, plainClient(), "http://"+addr+"/ping")

	if resp, err := insecureTLSClient().Get("https://" + addr + "/ping"); err == nil {
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("HTTPS GET reached the handler after disabling TLS (status 200)")
		}
	}
}

// TestRebindSameAddrInvalidTLSKeepsOldListener guards R13-P10 in the new
// same-address path: an invalid cert/key pair must fail BEFORE the old
// listener is touched, so the running server is never taken down by a bad
// TLS config.
func TestRebindSameAddrInvalidTLSKeepsOldListener(t *testing.T) {
	addr, port := freeAddr(t)
	certPath, _ := writeSelfSignedTLS(t)
	missingKey := filepath.Join(t.TempDir(), "does-not-exist.key")
	reb, _ := wireRebinder(t, addr, port)
	waitServing(t, plainClient(), "http://"+addr+"/ping")

	err := reb.RebindWithTLS(addr, certPath, missingKey)
	if err == nil {
		t.Fatal("expected rebind error for invalid TLS pair, got nil")
	}

	waitServing(t, plainClient(), "http://"+addr+"/ping")
	if reb.curAddr != addr || reb.tlsCert != "" || reb.tlsEnabled {
		t.Fatalf("state changed after failed rebind: addr=%q cert=%q tls=%v", reb.curAddr, reb.tlsCert, reb.tlsEnabled)
	}
}

// TestRebindAddrChangeKeepsOldAlive locks the no-downtime path of a plain
// host change (Story 6-3 R13-P2): the new listener binds BEFORE the old
// one is shut down. Same port on a different loopback host (127.0.0.1 ->
// [::1]) so the firewall sync is a no-op in the test.
func TestRebindAddrChangeKeepsOldAlive(t *testing.T) {
	addr, port := freeAddr(t)
	reb, _ := wireRebinder(t, addr, port)
	waitServing(t, plainClient(), "http://"+addr+"/ping")

	newAddr := net.JoinHostPort("::1", strconv.Itoa(port))
	if err := reb.RebindWithTLS(newAddr, "", ""); err != nil {
		t.Fatalf("addr-change rebind failed: %v", err)
	}

	waitServing(t, plainClient(), "http://"+newAddr+"/ping")
	if resp, err := plainClient().Get("http://" + addr + "/ping"); err == nil {
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("old listener status %d during addr-change rebind", resp.StatusCode)
		}
	}
	if reb.curAddr != newAddr {
		t.Fatalf("curAddr = %q, want %q", reb.curAddr, newAddr)
	}
}

// TestRebindAddrChangeBindFailurePreservesOld is the classic R13-P10
// contract: when the new address cannot be bound, the old listener keeps
// running and Rebind returns an error.
func TestRebindAddrChangeBindFailurePreservesOld(t *testing.T) {
	addr, port := freeAddr(t)
	blocked, err := net.Listen("tcp", net.JoinHostPort("::1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("block ::1 port: %v", err)
	}
	defer blocked.Close()

	reb, _ := wireRebinder(t, addr, port)
	waitServing(t, plainClient(), "http://"+addr+"/ping")

	newAddr := net.JoinHostPort("::1", strconv.Itoa(port))
	if err := reb.RebindWithTLS(newAddr, "", ""); err == nil {
		t.Fatal("expected rebind error when new addr is occupied, got nil")
	}
	waitServing(t, plainClient(), "http://"+addr+"/ping")
	if reb.curAddr != addr {
		t.Fatalf("curAddr mutated after failed rebind: %q", reb.curAddr)
	}
}
