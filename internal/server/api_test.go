package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/WSB98/file-transfer/internal/store"
)

func TestAWholeUploadOverHTTP(t *testing.T) {
	const chunk = 1024
	e := newTestEnv(t, chunk, 1<<20)
	data := randomBytes(7*chunk + 1)

	status, info := e.create("backup.zip", int64(len(data)), 1700000000000)
	if status != http.StatusOK || info.Chunks != 8 || info.ChunkSize != chunk || len(info.Done) != 0 || info.Parallel != 2 || info.State != "receiving" {
		t.Fatalf("create: %d %+v", status, info)
	}
	var wg sync.WaitGroup
	for w := int64(0); w < 2; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := w; i < info.Chunks; i += 2 {
				if got := e.put(info.ID, i, chunkOf(data, i, chunk)); got != http.StatusNoContent {
					t.Errorf("chunk %d: %d", i, got)
				}
			}
		}()
	}
	wg.Wait()

	if got := e.finish(info.ID); got != http.StatusAccepted {
		t.Fatalf("finish: %d", got)
	}
	st := e.waitFor(info.ID, "done")
	want := sha256.Sum256(data)
	if st.SHA256 != hex.EncodeToString(want[:]) || st.Name != "backup.zip" || st.DoneBytes != int64(len(data)) {
		t.Errorf("status %+v", st)
	}
	if got, _ := os.ReadFile(filepath.Join(e.inbox, "backup.zip")); !bytes.Equal(got, data) {
		t.Error("saved file differs")
	}
	if _, again := e.create("backup.zip", int64(len(data)), 1700000000000); again.State != "done" {
		t.Errorf("re-picking a finished file: %+v", again)
	}
}

func TestResumeReportsDoneChunks(t *testing.T) {
	e := newTestEnv(t, 1024, 1<<20)
	data := randomBytes(3000)
	_, a := e.create("a.zip", 3000, 1)
	e.put(a.ID, 1, chunkOf(data, 1, 1024))
	if _, again := e.create("a.zip", 3000, 1); again.ID != a.ID || !slices.Equal(again.Done, []int64{1}) {
		t.Errorf("reopen: %+v", again)
	}
	if got := e.finish(a.ID); got != http.StatusConflict {
		t.Errorf("finish with chunks missing: %d, want 409", got)
	}
}

func TestChunkRequestValidation(t *testing.T) {
	e := newTestEnv(t, 1024, 1<<20)
	data := randomBytes(2048 + 10)
	_, a := e.create("a.zip", int64(len(data)), 1)
	good := chunkOf(data, 0, 1024)
	goodHash := sha256.Sum256(good)

	cases := []struct {
		name string
		path string
		body []byte
		hash string
		want int
	}{
		{"index past end", "/chunks/3", good, "", http.StatusBadRequest},
		{"negative index", "/chunks/-1", good, "", http.StatusBadRequest},
		{"non-numeric index", "/chunks/abc", good, "", http.StatusBadRequest},
		{"huge index", "/chunks/99999999999999999999", good, "", http.StatusBadRequest},
		{"short body", "/chunks/0", good[:1000], "", http.StatusBadRequest},
		{"long body", "/chunks/0", append(slices.Clone(good), 1), "", http.StatusBadRequest},
		{"last chunk at full size", "/chunks/2", good, "", http.StatusBadRequest},
		{"missing hash", "/chunks/0", good, "none", http.StatusBadRequest},
		{"short hash", "/chunks/0", good, "abcd", http.StatusBadRequest},
		{"non-hex hash", "/chunks/0", good, strings.Repeat("zz", 32), http.StatusBadRequest},
		{"wrong hash", "/chunks/0", good, strings.Repeat("ab", 32), http.StatusUnprocessableEntity},
		{"right hash", "/chunks/0", good, hex.EncodeToString(goodHash[:]), http.StatusNoContent},
	}
	for _, c := range cases {
		h := auth()
		switch c.hash {
		case "":
			h["X-Chunk-SHA256"] = strings.Repeat("ab", 32)
		case "none":
		default:
			h["X-Chunk-SHA256"] = c.hash
		}
		if resp, _ := e.do("PUT", "/api/uploads/"+a.ID+c.path, c.body, h); resp.StatusCode != c.want {
			t.Errorf("%s: got %d, want %d", c.name, resp.StatusCode, c.want)
		}
	}
	if got := e.put("0123456789abcdef0123456789abcdef", 0, good); got != http.StatusNotFound {
		t.Errorf("unknown upload: got %d, want 404", got)
	}
}

func TestCreateValidation(t *testing.T) {
	e := newTestEnv(t, 1024, 10_000)
	for _, c := range []struct {
		name string
		body string
		want int
	}{
		{"zero size", `{"name":"a","size":0,"modified":1}`, http.StatusBadRequest},
		{"negative size", `{"name":"a","size":-5,"modified":1}`, http.StatusBadRequest},
		{"bad json", `{"name":`, http.StatusBadRequest},
		{"unknown field", `{"name":"a","size":5,"modified":1,"path":"../x"}`, http.StatusBadRequest},
		{"oversize body", `{"name":"` + strings.Repeat("a", 5000) + `","size":5,"modified":1}`, http.StatusBadRequest},
		{"over the run's cap", `{"name":"a","size":10001,"modified":1}`, http.StatusRequestEntityTooLarge},
		{"traversal name is fine, it gets cleaned", `{"name":"../../evil.exe","size":5,"modified":1}`, http.StatusOK},
	} {
		if resp, _ := e.do("POST", "/api/uploads", []byte(c.body), auth()); resp.StatusCode != c.want {
			t.Errorf("%s: got %d, want %d", c.name, resp.StatusCode, c.want)
		}
	}
}

func TestBusyAndDuplicateChunks(t *testing.T) {
	e := newTestEnv(t, 1024, 1<<20)
	data := randomBytes(2048)
	_, a := e.create("a.zip", 2048, 1)

	for range cap(e.srv.slots) {
		e.srv.slots <- struct{}{}
	}
	h := auth()
	h["X-Chunk-SHA256"] = strings.Repeat("ab", 32)
	resp, _ := e.do("PUT", "/api/uploads/"+a.ID+"/chunks/1", chunkOf(data, 1, 1024), h)
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") == "" {
		t.Errorf("server full: got %d with Retry-After %q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	for range cap(e.srv.slots) {
		<-e.srv.slots
	}
	if got := e.put(a.ID, 1, chunkOf(data, 1, 1024)); got != http.StatusNoContent {
		t.Errorf("after slots free: %d", got)
	}
	if got := e.put(a.ID, 1, chunkOf(data, 1, 1024)); got != http.StatusNoContent {
		t.Errorf("resending a done chunk should be a no-op success: %d", got)
	}
}

func TestEveryStoreErrorHasAnAnswer(t *testing.T) {
	e := newTestEnv(t, 1024, 1<<20)
	for _, m := range storeErrors {
		wrapped := fmt.Errorf("context: %w", m.err)
		if got := e.srv.apiErrorFor(wrapped); got != m.api {
			t.Errorf("%v maps to %v, want %v", m.err, got, m.api)
		}
	}
	if got := e.srv.apiErrorFor(errors.New("disk on fire")); got != errInternal {
		t.Errorf("an unexpected error maps to %v", got)
	}
	if got := e.logged("internal error"); len(got) != 1 {
		t.Errorf("an unexpected error should be logged once, got %q", got)
	}
	if store.ErrNoSpace.Error() == "" {
		t.Error("store errors need messages")
	}
}

// FuzzCreateRequest throws arbitrary bodies at the create endpoint: whatever
// arrives, the answer is a client error or success, never a crash or a 500.
// Each input gets a fresh store, so it reaches the real create path, and the
// handler is called in memory, since a socket per input exhausts local ports.
func FuzzCreateRequest(f *testing.F) {
	for _, s := range []string{`{"name":"a","size":5,"modified":1}`, `{}`, `[]`, `{"size":1e400}`, `{"name":null}`, "\x00", `{"size":-9223372036854775808}`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, body string) {
		st, err := store.New(store.Options{Inbox: t.TempDir(), ChunkSize: 1024, MaxTotal: 1 << 20, Logf: func(string, ...any) {}})
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		h := New(Config{Token: testToken, Key: testKey, Store: st, Parallel: 2, Logf: func(string, ...any) {}}).Handler()
		req := httptest.NewRequest("POST", "/api/uploads", strings.NewReader(body))
		for k, v := range auth() {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code >= 500 {
			t.Fatalf("body %q: got %d %s", body, rec.Code, rec.Body)
		}
	})
}
