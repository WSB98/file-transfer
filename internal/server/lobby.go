package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	maxPeople   = 64
	maxNameLen  = 32
	idleAfter   = 15 * time.Second // no poll for this long shows a person as away
	forgetAfter = 10 * time.Minute // and this long removes them
	rateWindow  = 10 * time.Second
)

var (
	errTooManyPeople  = &apiError{http.StatusTooManyRequests, "too_many_people"}
	errUnknownSession = &apiError{http.StatusConflict, "unknown_session"}
)

// lobby tracks who has joined this run and how fast each transfer moves.
// Everyone holding the key sees all of it: names, addresses, latencies, and
// each transfer's name, size, and progress. Never file contents.
type lobby struct {
	mu      sync.Mutex
	people  map[string]*person // by session id
	senders map[string]string  // upload id -> name of whoever last opened it
	rates   map[string][]sample
	joins   int // people admitted so far, which orders the list
}

type person struct {
	name string
	ip   string
	rtt  int64 // milliseconds, as the person's own browser measured it
	// order is the person's place in the join sequence. Join times can tie
	// on coarse clocks, which would let the list reshuffle between polls.
	order    int
	lastSeen time.Time
}

type sample struct {
	at    time.Time
	moved int64
}

func newLobby() *lobby {
	return &lobby{
		people:  make(map[string]*person),
		senders: make(map[string]string),
		rates:   make(map[string][]sample),
	}
}

// cleanName keeps a typed display name to printable characters, trimmed and
// short. It reaches other people's pages (as text, never markup) and this
// terminal, so control and formatting characters, including the ones that
// reverse text direction, are dropped.
func cleanName(raw string) string {
	var b strings.Builder
	n := 0
	for _, r := range raw {
		if n == maxNameLen {
			break
		}
		if !unicode.IsPrint(r) {
			continue
		}
		b.WriteRune(r)
		n++
	}
	name := strings.Join(strings.Fields(b.String()), " ")
	if name == "" {
		return "Guest"
	}
	return name
}

func newSessionID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (l *lobby) join(id, name, ip string, now time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.forget(now)
	if len(l.people) >= maxPeople {
		return errTooManyPeople
	}
	l.joins++
	l.people[id] = &person{name: name, ip: ip, order: l.joins, lastSeen: now}
	return nil
}

// seen records a poll from a session, with the round trip its browser
// measured on the previous one. It reports false for an unknown session.
func (l *lobby) seen(id string, rtt int64, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.forget(now)
	p, ok := l.people[id]
	if !ok {
		return false
	}
	p.lastSeen = now
	if rtt > 0 && rtt < 600_000 {
		p.rtt = rtt
	}
	return true
}

func (l *lobby) forget(now time.Time) {
	for id, p := range l.people {
		if now.Sub(p.lastSeen) > forgetAfter {
			delete(l.people, id)
		}
	}
}

func (l *lobby) nameOf(session string) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	p, ok := l.people[session]
	if !ok {
		return "", false
	}
	return p.name, true
}

func (l *lobby) setSender(uploadID, name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.senders[uploadID] = name
}

func (l *lobby) senderOf(uploadID string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.senders[uploadID]
}

// rate is how fast an upload moved over the last ten seconds, in bytes per
// second. Samples are taken as people poll, at most one a second, so it needs
// no timer of its own.
func (l *lobby) rate(uploadID string, moved int64, now time.Time) float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.rates[uploadID]
	if len(s) == 0 || now.Sub(s[len(s)-1].at) >= time.Second {
		s = append(s, sample{now, moved})
	}
	for len(s) > 2 && now.Sub(s[0].at) > rateWindow {
		s = s[1:]
	}
	l.rates[uploadID] = s
	first, last := s[0], s[len(s)-1]
	if dt := last.at.Sub(first.at).Seconds(); dt > 0 && now.Sub(last.at) < 2*time.Second {
		return float64(last.moved-first.moved) / dt
	}
	return 0
}

type personView struct {
	Name string `json:"name"`
	IP   string `json:"ip"`
	RTT  int64  `json:"rtt"`
	Idle bool   `json:"idle"`
	You  bool   `json:"you"`
}

type transferView struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Sender   string  `json:"sender"`
	State    string  `json:"state"`
	Size     int64   `json:"size"`
	Received int64   `json:"received"`
	Verified int64   `json:"verified"`
	Rate     float64 `json:"rate"`
	SHA256   string  `json:"sha256,omitempty"`
}

type lobbyView struct {
	People    []personView   `json:"people"`
	Transfers []transferView `json:"transfers"`
}

func (l *lobby) peopleView(you string, now time.Time) []personView {
	l.mu.Lock()
	defer l.mu.Unlock()
	ids := make([]string, 0, len(l.people))
	for id := range l.people {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return l.people[ids[i]].order < l.people[ids[j]].order })
	view := make([]personView, 0, len(ids))
	for _, id := range ids {
		p := l.people[id]
		view = append(view, personView{Name: p.name, IP: p.ip, RTT: p.rtt, Idle: now.Sub(p.lastSeen) > idleAfter, You: id == you})
	}
	return view
}

// handleJoin admits someone to the lobby. Reaching it means the token and the
// key were both right; the name is only a label.
func (s *Server) handleJoin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		s.fail(w, errBadRequest)
		return
	}
	id, err := newSessionID()
	if err != nil {
		s.fail(w, err)
		return
	}
	name := cleanName(req.Name)
	if err := s.lobby.join(id, name, peerIP(r), time.Now()); err != nil {
		s.fail(w, err)
		return
	}
	s.logf("%s (%s) joined the lobby", name, peerIP(r))
	writeJSON(w, http.StatusOK, map[string]string{"session": id, "name": name})
}

// handleLobby is the page's heartbeat: it marks the caller present, records
// the round trip its browser measured last time, and returns the lobby.
func (s *Server) handleLobby(w http.ResponseWriter, r *http.Request) {
	session := r.Header.Get("X-Drop-Session")
	rtt, _ := strconv.ParseInt(r.Header.Get("X-Drop-RTT"), 10, 64)
	now := time.Now()
	if !s.lobby.seen(session, rtt, now) {
		s.fail(w, errUnknownSession)
		return
	}
	view := lobbyView{People: s.lobby.peopleView(session, now), Transfers: []transferView{}}
	for _, u := range s.store.Uploads() {
		st := u.Status()
		view.Transfers = append(view.Transfers, transferView{
			ID:       u.ID(),
			Name:     st.Name,
			Sender:   s.lobby.senderOf(u.ID()),
			State:    string(st.Phase),
			Size:     st.Size,
			Received: st.Received,
			Verified: st.Verified,
			Rate:     s.lobby.rate(u.ID(), u.Moved(), now),
			SHA256:   st.SHA256,
		})
	}
	writeJSON(w, http.StatusOK, view)
}
