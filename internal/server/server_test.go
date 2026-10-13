package server

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/WSB98/file-transfer/internal/creds"
	"github.com/WSB98/file-transfer/internal/store"
)

func TestWithoutTheTokenEverythingIsAnEmpty404(t *testing.T) {
	e := newTestEnv(t, 1024, 1<<20)
	cases := []struct {
		name, method, path string
		header             map[string]string
	}{
		{"root", "GET", "/", nil},
		{"favicon", "GET", "/favicon.ico", nil},
		{"page without token", "GET", "/t/", nil},
		{"page with wrong token", "GET", "/t/wrong/", nil},
		{"page with token prefix", "GET", "/t/" + testToken[:10] + "/", nil},
		{"api without token", "POST", "/api/uploads", nil},
		{"api with key but no token", "POST", "/api/uploads", map[string]string{"X-Drop-Key": testKey}},
		{"api with wrong token", "POST", "/api/uploads", map[string]string{"Authorization": "Bearer wrong", "X-Drop-Key": testKey}},
		{"api with token, wrong scheme", "POST", "/api/uploads", map[string]string{"Authorization": "Basic " + testToken}},
		{"api with token in query", "POST", "/api/uploads?token=" + testToken, nil},
		{"api with a longer token", "GET", "/api/lobby", map[string]string{"Authorization": "Bearer " + testToken + "x"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, body := e.do(c.method, c.path, []byte(`{}`), c.header)
			if resp.StatusCode != http.StatusNotFound || len(body) != 0 {
				t.Errorf("got %d with %d body bytes, want an empty 404", resp.StatusCode, len(body))
			}
			if resp.Header.Get("Server") != "" {
				t.Errorf("Server header %q leaks", resp.Header.Get("Server"))
			}
		})
	}
	if got, _ := os.ReadDir(e.inbox + "/" + store.WorkDir); len(got) != 0 {
		t.Errorf("unauthenticated requests created %v", got)
	}
}

func TestUnknownRoutesLookTheSameEvenWithTheToken(t *testing.T) {
	e := newTestEnv(t, 1024, 1<<20)
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/nothing"},
		{"DELETE", "/api/uploads/abc"},
		{"GET", "/api/uploads"},
		{"POST", "/t/" + testToken + "/"},
		{"GET", "/t/" + testToken + "/x"},
		{"GET", "/t/" + testToken + "/.."},
		{"GET", "/t/" + testToken + "/../api/lobby"},
	} {
		resp, body := e.do(c.method, c.path, nil, auth())
		if resp.StatusCode != http.StatusNotFound || len(body) != 0 {
			t.Errorf("%s %s: got %d with %d body bytes, want an empty 404", c.method, c.path, resp.StatusCode, len(body))
		}
	}
}

func TestTheKeyIsRequiredForTheAPIButNotThePage(t *testing.T) {
	e := newTestEnv(t, 1024, 1<<20)
	body := []byte(`{"name":"a","size":5,"modified":1}`)
	for name, key := range map[string]string{"no key": "", "wrong key": "ZZZZ-ZZZZ-ZZZZ"} {
		h := map[string]string{"Authorization": "Bearer " + testToken}
		if key != "" {
			h["X-Drop-Key"] = key
		}
		if resp, _ := e.do("POST", "/api/uploads", body, h); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: got %d, want 401", name, resp.StatusCode)
		}
	}
	if got := e.logged("WARNING"); len(got) != 1 {
		t.Errorf("two wrong keys inside the throttle window should log one warning, got %q", got)
	}
	if resp, _ := e.do("POST", "/api/uploads", body, map[string]string{"Authorization": "Bearer " + testToken, "X-Drop-Key": "abcd efgh jkmn"}); resp.StatusCode != http.StatusOK {
		t.Errorf("a loosely typed key: got %d, want 200", resp.StatusCode)
	}
	if resp, _ := e.do("POST", "/api/join", []byte(`{"name":"x"}`), auth()); resp.StatusCode != http.StatusOK {
		t.Errorf("join with the key: got %d, want 200", resp.StatusCode)
	}
	if resp, _ := e.do("GET", "/t/"+testToken+"/", nil, nil); resp.StatusCode != http.StatusOK {
		t.Errorf("page: got %d, want 200", resp.StatusCode)
	}
}

func TestPageFilesAndSecurityHeaders(t *testing.T) {
	e := newTestEnv(t, 1024, 1<<20)
	for path, ctype := range map[string]string{
		"":         "text/html; charset=utf-8",
		"app.js":   "text/javascript; charset=utf-8",
		"app.css":  "text/css; charset=utf-8",
		"icon.svg": "image/svg+xml",
	} {
		resp, body := e.do("GET", "/t/"+testToken+"/"+path, nil, nil)
		if resp.StatusCode != http.StatusOK || len(body) == 0 {
			t.Fatalf("%q: got %d", path, resp.StatusCode)
		}
		if got := resp.Header.Get("Content-Type"); got != ctype {
			t.Errorf("%q: Content-Type %q", path, got)
		}
		for _, h := range []string{"Content-Security-Policy", "Referrer-Policy", "X-Content-Type-Options", "Cache-Control"} {
			if resp.Header.Get(h) == "" {
				t.Errorf("%q: missing %s", path, h)
			}
		}
	}
}

func startTLS(t *testing.T) (addr string, cert tls.Certificate) {
	t.Helper()
	cert, err := creds.NewCert([]string{"127.0.0.1"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	e := newTestEnv(t, 1024, 1<<20)
	srv := HTTPServer(e.srv.Handler(), cert)
	go srv.ServeTLS(ln, "", "")
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().String(), cert
}

func TestTLSServesHTTP1Only(t *testing.T) {
	addr, cert := startTLS(t)
	roots := x509.NewCertPool()
	roots.AddCert(cert.Leaf)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}, ForceAttemptHTTP2: true}}
	resp, err := client.Get("https://" + addr + "/t/" + testToken + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Proto != "HTTP/1.1" || resp.TLS.Version < tls.VersionTLS12 {
		t.Errorf("status %d, %s, TLS %x: want 200 over HTTP/1.1 and TLS 1.2+", resp.StatusCode, resp.Proto, resp.TLS.Version)
	}

	old := &tls.Config{InsecureSkipVerify: true, MaxVersion: tls.VersionTLS11}
	if conn, err := tls.Dial("tcp", addr, old); err == nil {
		conn.Close()
		t.Error("a TLS 1.1 handshake was accepted")
	}
}

func TestSlowHeadersAreCut(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the header timeout")
	}
	addr, _ := startTLS(t)
	conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	io.WriteString(conn, "GET / HTTP/1.1\r\nHost: x\r\n") // never finishes the headers

	start := time.Now()
	conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	io.ReadAll(conn)
	if waited := time.Since(start); waited > 15*time.Second {
		t.Errorf("connection held for %v; ReadHeaderTimeout should cut it near 10s", waited)
	}
}

func TestAStalledChunkIsReleasedAndLoggedOnce(t *testing.T) {
	e := newTestEnv(t, 1024, 1<<20)
	e.srv.chunkIdle = 300 * time.Millisecond
	data := randomBytes(1024)
	_, info := e.create("a.zip", 1024, 1)

	// Send headers and half the body, then go silent, like a dropped Wi-Fi link.
	conn, err := net.Dial("tcp", strings.TrimPrefix(e.url, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	sum := sha256.Sum256(data)
	fmt.Fprintf(conn, "PUT /api/uploads/%s/chunks/0 HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer %s\r\nX-Drop-Key: %s\r\nX-Chunk-SHA256: %s\r\nContent-Length: 1024\r\n\r\n",
		info.ID, testToken, testKey, hex.EncodeToString(sum[:]))
	conn.Write(data[:512])

	// Wait until the server is reading the stalled request: it holds a slot.
	// Watching the slots, rather than probing with requests, can't race it.
	for end := time.Now().Add(5 * time.Second); len(e.srv.slots) == 0; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("the stalled request never arrived")
		}
	}
	time.Sleep(50 * time.Millisecond) // from taking the slot to claiming the chunk
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		got := e.put(info.ID, 0, data)
		if got == http.StatusNoContent {
			break
		}
		if got != http.StatusConflict {
			t.Fatalf("retry: got %d", got)
		}
		if time.Now().After(end) {
			t.Fatal("the stalled chunk was never released")
		}
	}
	var cutOff []string
	for _, l := range e.logged("chunk ") {
		if strings.Contains(l, "cut off") {
			cutOff = append(cutOff, l)
		}
	}
	if len(cutOff) != 1 || len(e.logged("internal error")) != 0 {
		t.Errorf("a stalled chunk should log one calm line and nothing alarming, got %q", e.logs)
	}
}
