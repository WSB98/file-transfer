package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func (e *testEnv) join(name string) string {
	e.t.Helper()
	body, _ := json.Marshal(map[string]string{"name": name})
	resp, b := e.do("POST", "/api/join", body, auth())
	if resp.StatusCode != http.StatusOK {
		e.t.Fatalf("join: %d %s", resp.StatusCode, b)
	}
	var out struct{ Session, Name string }
	json.Unmarshal(b, &out)
	return out.Session
}

func (e *testEnv) lobby(session string, rtt int) (int, lobbyView) {
	e.t.Helper()
	h := auth()
	h["X-Drop-Session"] = session
	if rtt > 0 {
		h["X-Drop-RTT"] = strconv.Itoa(rtt)
	}
	resp, b := e.do("GET", "/api/lobby", nil, h)
	var v lobbyView
	json.Unmarshal(b, &v)
	return resp.StatusCode, v
}

func TestJoiningNeedsTheKey(t *testing.T) {
	e := newTestEnv(t, 1024, 1<<20)
	if resp, _ := e.do("POST", "/api/join", []byte(`{"name":"x"}`), map[string]string{"Authorization": "Bearer " + testToken, "X-Drop-Key": "ZZZZ-ZZZZ-ZZZZ"}); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong key: got %d, want 401", resp.StatusCode)
	}
	if resp, b := e.do("POST", "/api/join", []byte(`{"name":"x","admin":true}`), auth()); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown field: got %d %s, want 400", resp.StatusCode, b)
	}
}

func TestPeopleStayInJoinOrderEvenWhenTheClockTies(t *testing.T) {
	l := newLobby()
	now := time.Now()
	names := []string{"Ana", "Ben", "Cy", "Dee", "Eli", "Flo", "Gus", "Hal"}
	for i, name := range names {
		if err := l.join(fmt.Sprint("session", i), name, "127.0.0.1", now); err != nil {
			t.Fatal(err)
		}
	}
	for range 20 {
		view := l.peopleView("", now)
		for i, p := range view {
			if p.Name != names[i] {
				t.Fatalf("position %d is %s, want %s", i, p.Name, names[i])
			}
		}
	}
}

func TestTheLobbyShowsPeopleAndTransfers(t *testing.T) {
	e := newTestEnv(t, 1024, 1<<20)
	alex := e.join("  Alex‮\x07 ")
	sam := e.join("Sam")

	status, v := e.lobby(alex, 42)
	if status != http.StatusOK || len(v.People) != 2 {
		t.Fatalf("lobby: %d %+v", status, v)
	}
	if p := v.People[0]; p.Name != "Alex" || !p.You || p.IP != "127.0.0.1" || p.RTT != 42 || p.Idle {
		t.Errorf("first person %+v", p)
	}
	if p := v.People[1]; p.Name != "Sam" || p.You {
		t.Errorf("second person %+v", p)
	}
	if !strings.Contains(strings.Join(e.logged(""), "\n"), "Alex (127.0.0.1) joined the lobby") {
		t.Error("joining was not logged")
	}

	// Alex starts an upload; Sam sees it, attributed to Alex, with its progress.
	data := randomBytes(3000)
	body, _ := json.Marshal(map[string]any{"name": "photos.zip", "size": 3000, "modified": 1})
	h := auth()
	h["X-Drop-Session"] = alex
	resp, b := e.do("POST", "/api/uploads", body, h)
	var info uploadInfo
	json.Unmarshal(b, &info)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create: %d", resp.StatusCode)
	}
	e.put(info.ID, 0, chunkOf(data, 0, 1024))

	_, v = e.lobby(sam, 0)
	if len(v.Transfers) != 1 {
		t.Fatalf("transfers %+v", v.Transfers)
	}
	tr := v.Transfers[0]
	if tr.Name != "photos.zip" || tr.Sender != "Alex" || tr.Size != 3000 || tr.Received != 1024 || tr.State != "receiving" {
		t.Errorf("transfer %+v", tr)
	}
	if _, v = e.lobby(sam, 0); v.People[0].RTT != 42 {
		t.Error("Alex's latency should be visible to Sam")
	}
}

func TestTheLobbyNeedsAKnownSession(t *testing.T) {
	e := newTestEnv(t, 1024, 1<<20)
	for _, session := range []string{"", "nope", strings.Repeat("0", 32)} {
		if status, _ := e.lobby(session, 0); status != http.StatusConflict {
			t.Errorf("session %q: got %d, want 409", session, status)
		}
	}
}

func TestCleanName(t *testing.T) {
	for in, want := range map[string]string{
		"Alex":                  "Alex",
		"  Alex   Smith  ":      "Alex Smith",
		"":                      "Guest",
		" \t\n ":                "Guest",
		"evil‮exe.txt":          "evilexe.txt",
		"bell\x07\x1b[31mred":   "bell[31mred",
		"José 🚀":                "José 🚀",
		strings.Repeat("a", 50): strings.Repeat("a", maxNameLen),
		strings.Repeat("é", 50): strings.Repeat("é", maxNameLen),
	} {
		if got := cleanName(in); got != want {
			t.Errorf("cleanName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPeopleAreCappedAndForgotten(t *testing.T) {
	l := newLobby()
	now := time.Now()
	for i := range maxPeople {
		if err := l.join(fmt.Sprint(i), "p", "ip", now); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.join("one more", "p", "ip", now); err != errTooManyPeople {
		t.Fatalf("person %d: %v", maxPeople+1, err)
	}
	l.seen("0", 0, now.Add(forgetAfter/2))
	for _, p := range l.peopleView("0", now.Add(forgetAfter/2)) {
		if p.Idle == p.You {
			t.Fatalf("%+v: only the one who kept polling should be present", p)
		}
	}
	later := now.Add(forgetAfter + time.Second)
	if err := l.join("new", "p", "ip", later); err != nil {
		t.Fatalf("after the silent ones are forgotten: %v", err)
	}
	if n := len(l.peopleView("", later)); n != 2 {
		t.Errorf("%d people remain, want the recent poller and the newcomer", n)
	}
}

func TestRateFollowsRecentProgress(t *testing.T) {
	l := newLobby()
	t0 := time.Now()
	if r := l.rate("u", 0, t0); r != 0 {
		t.Errorf("first sample: %v", r)
	}
	if r := l.rate("u", 4000, t0.Add(2*time.Second)); r != 2000 {
		t.Errorf("4000 bytes in 2 s: %v, want 2000", r)
	}
	var r float64
	for s := 3; s <= 15; s++ {
		r = l.rate("u", 4000, t0.Add(time.Duration(s)*time.Second))
	}
	if r != 0 {
		t.Errorf("after 12 s without progress: %v, want 0", r)
	}
}
