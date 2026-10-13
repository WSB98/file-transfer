package server

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/WSB98/file-transfer/internal/creds"
	"github.com/WSB98/file-transfer/internal/store"
)

// TestLoopbackThroughput is verification gate 3: a client over real TLS on
// loopback, uploading in parallel like the browser does, must far outrun any
// home internet link. Opt in, because it writes gigabytes:
//
//	DROP_BENCH=1 [DROP_BENCH_GIB=4] [DROP_BENCH_DIR=D:\somewhere] go test -run Loopback -v
func TestLoopbackThroughput(t *testing.T) {
	if os.Getenv("DROP_BENCH") == "" {
		t.Skip("set DROP_BENCH=1 to run")
	}
	gib, _ := strconv.Atoi(os.Getenv("DROP_BENCH_GIB"))
	if gib <= 0 {
		gib = 2
	}
	dir := os.Getenv("DROP_BENCH_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	const chunk, parallel = 32 << 20, 4
	size := int64(gib) << 30

	cert, _ := creds.NewCert([]string{"127.0.0.1"}, time.Now())
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	st, err := store.New(store.Options{Inbox: dir, ChunkSize: chunk, MaxTotal: size, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := HTTPServer(New(Config{Token: testToken, Key: testKey, Store: st, Parallel: parallel, Logf: t.Logf}).Handler(), cert)
	go srv.ServeTLS(ln, "", "")
	defer srv.Close()

	roots := x509.NewCertPool()
	roots.AddCert(cert.Leaf)
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig:     &tls.Config{RootCAs: roots},
		MaxConnsPerHost:     parallel,
		MaxIdleConnsPerHost: parallel,
	}}
	base := "https://" + ln.Addr().String()
	send := func(method, path string, body []byte, extra map[string]string) *http.Response {
		req, _ := http.NewRequest(method, base+path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("X-Drop-Key", testKey)
		for k, v := range extra {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	// One random chunk repeated: the server still hashes and writes every byte,
	// and the client skips hashing gigabytes of fresh data each run.
	block := make([]byte, chunk)
	rand.Read(block)
	sum := sha256.Sum256(block)
	blockHash := hex.EncodeToString(sum[:])

	name := fmt.Sprintf("bench-%d.bin", time.Now().UnixNano())
	resp := send("POST", "/api/uploads", []byte(fmt.Sprintf(`{"name":%q,"size":%d,"modified":1}`, name, size)), nil)
	var info uploadInfo
	json.NewDecoder(resp.Body).Decode(&info)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create: %d", resp.StatusCode)
	}

	start := time.Now()
	next := make(chan int64)
	var wg sync.WaitGroup
	for range parallel {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				r := send("PUT", fmt.Sprintf("/api/uploads/%s/chunks/%d", info.ID, i), block, map[string]string{"X-Chunk-SHA256": blockHash})
				r.Body.Close()
				if r.StatusCode != http.StatusNoContent {
					t.Errorf("chunk %d: %d", i, r.StatusCode)
				}
			}
		}()
	}
	for i := range info.Chunks {
		next <- i
	}
	close(next)
	wg.Wait()
	upload := time.Since(start)

	start = time.Now()
	send("POST", "/api/uploads/"+info.ID+"/finish", nil, nil).Body.Close()
	u, _ := st.Lookup(info.ID)
	for u.Status().Phase == store.Verifying {
		time.Sleep(50 * time.Millisecond)
	}
	verify := time.Since(start)
	if s := u.Status(); s.Phase != store.Done {
		t.Fatalf("ended %s", s.Phase)
	} else {
		os.Remove(filepath.Join(dir, s.Name))
	}

	mbps := func(d time.Duration) float64 { return float64(size) / d.Seconds() / 1e6 }
	t.Logf("%d GiB into %s", gib, dir)
	t.Logf("upload: %.1f s, %.0f MB/s (%.1f Gbit/s)", upload.Seconds(), mbps(upload), mbps(upload)*8/1000)
	t.Logf("verify: %.1f s, %.0f MB/s", verify.Seconds(), mbps(verify))
}
