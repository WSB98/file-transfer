package server

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/WSB98/file-transfer/internal/store"
)

const (
	testToken = "test-token-0123456789abcdefghijklmnopqrstu"
	testKey   = "ABCD-EFGH-JKMN"
)

// testEnv is a real HTTP server on loopback in front of a store in a temp
// inbox. Plain HTTP keeps these tests fast; TLS has its own test.
type testEnv struct {
	t      testing.TB
	inbox  string
	store  *store.Store
	srv    *Server
	url    string
	client *http.Client

	mu   sync.Mutex
	logs []string
}

func newTestEnv(t testing.TB, chunkSize, maxTotal int64) *testEnv {
	t.Helper()
	e := &testEnv{t: t, inbox: t.TempDir()}
	st, err := store.New(store.Options{Inbox: e.inbox, ChunkSize: chunkSize, MaxTotal: maxTotal, Logf: e.logf})
	if err != nil {
		t.Fatal(err)
	}
	e.store = st
	e.srv = New(Config{Token: testToken, Key: testKey, Store: st, Parallel: 2, Logf: e.logf})
	ts := httptest.NewServer(e.srv.Handler())
	e.url, e.client = ts.URL, ts.Client()
	t.Cleanup(func() {
		ts.Close()
		st.Close()
	})
	return e
}

func (e *testEnv) logf(format string, args ...any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	line := fmt.Sprintf(format, args...)
	e.logs = append(e.logs, line)
	e.t.Log(line)
}

func (e *testEnv) logged(prefix string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, l := range e.logs {
		if strings.HasPrefix(l, prefix) {
			out = append(out, l)
		}
	}
	return out
}

func (e *testEnv) do(method, path string, body []byte, header map[string]string) (*http.Response, []byte) {
	e.t.Helper()
	req, err := http.NewRequest(method, e.url+path, bytes.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func auth() map[string]string {
	return map[string]string{"Authorization": "Bearer " + testToken, "X-Drop-Key": testKey}
}

func (e *testEnv) create(name string, size, modified int64) (int, uploadInfo) {
	e.t.Helper()
	body, _ := json.Marshal(map[string]any{"name": name, "size": size, "modified": modified})
	resp, b := e.do("POST", "/api/uploads", body, auth())
	var info uploadInfo
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(b, &info); err != nil {
			e.t.Fatal(err)
		}
	}
	return resp.StatusCode, info
}

func (e *testEnv) put(id string, i int64, data []byte) int {
	e.t.Helper()
	sum := sha256.Sum256(data)
	return e.putWithHash(id, i, data, hex.EncodeToString(sum[:]))
}

func (e *testEnv) putWithHash(id string, i int64, data []byte, hash string) int {
	e.t.Helper()
	h := auth()
	h["X-Chunk-SHA256"] = hash
	resp, _ := e.do("PUT", "/api/uploads/"+id+"/chunks/"+strconv.FormatInt(i, 10), data, h)
	return resp.StatusCode
}

func (e *testEnv) status(id string) statusInfo {
	e.t.Helper()
	resp, body := e.do("GET", "/api/uploads/"+id, nil, auth())
	if resp.StatusCode != http.StatusOK {
		e.t.Fatalf("status: %d", resp.StatusCode)
	}
	var st statusInfo
	json.Unmarshal(body, &st)
	return st
}

func (e *testEnv) finish(id string) int {
	e.t.Helper()
	resp, _ := e.do("POST", "/api/uploads/"+id+"/finish", nil, auth())
	return resp.StatusCode
}

func (e *testEnv) waitFor(id, want string) statusInfo {
	e.t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if st := e.status(id); st.State != "verifying" {
			if st.State != want {
				e.t.Fatalf("state %q, want %q", st.State, want)
			}
			return st
		}
	}
	e.t.Fatal("still verifying after 5s")
	return statusInfo{}
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

func chunkOf(data []byte, i, size int64) []byte {
	return data[i*size : min(int64(len(data)), (i+1)*size)]
}
