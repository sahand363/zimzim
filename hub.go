package main

import (
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

type outMsg struct {
	op   int
	data []byte
}

type client struct {
	walkieID string
	ws       *wsConn
	out      chan outMsg
	talking  atomic.Bool
	lastPage time.Time
	once     sync.Once
	done     chan struct{}
}

func (c *client) send(op int, data []byte) {
	select {
	case <-c.done:
		return
	case c.out <- outMsg{op, data}:
	default: // slow receiver: drop
	}
}

func (c *client) close() {
	c.once.Do(func() { close(c.done); c.ws.Close() })
}

type Hub struct {
	store   *Store
	mu      sync.RWMutex
	clients map[*client]bool
	calls   *callState
}

func NewHub(s *Store) *Hub {
	return &Hub{store: s, clients: map[*client]bool{}, calls: newCallState()}
}

// shared returns the codes that are ON on both walkies (case-insensitive).
func sharedCodes(a, b *Walkie) []string {
	if a == nil || b == nil {
		return nil
	}
	on := map[string]bool{}
	for _, c := range a.Codes {
		if c.On {
			on[strings.ToLower(c.Code)] = true
		}
	}
	var out []string
	for _, c := range b.Codes {
		if c.On && on[strings.ToLower(c.Code)] {
			out = append(out, c.Code)
		}
	}
	return out
}

// peersOf returns connected clients that share an ON code with walkie id (other walkies only).
func (h *Hub) peersOf(id string) []*client {
	h.store.mu.RLock()
	me := h.store.WalkieByID(id)
	h.mu.RLock()
	var out []*client
	for c := range h.clients {
		if c.walkieID == id {
			continue
		}
		if len(sharedCodes(me, h.store.WalkieByID(c.walkieID))) > 0 {
			out = append(out, c)
		}
	}
	h.mu.RUnlock()
	h.store.mu.RUnlock()
	return out
}

func (h *Hub) nameOf(id string) string {
	h.store.mu.RLock()
	defer h.store.mu.RUnlock()
	if w := h.store.WalkieByID(id); w != nil {
		return w.Name
	}
	return "?"
}

type peerInfo struct {
	Name    string   `json:"name"`
	Codes   []string `json:"codes"`
	Talking bool     `json:"talking"`
}

// broadcastState sends each client its own codes and the peers it can reach.
func (h *Hub) broadcastState() {
	h.mu.RLock()
	list := make([]*client, 0, len(h.clients))
	for c := range h.clients {
		list = append(list, c)
	}
	h.mu.RUnlock()
	h.store.mu.RLock()
	defer h.store.mu.RUnlock()
	for _, c := range list {
		me := h.store.WalkieByID(c.walkieID)
		if me == nil {
			continue
		}
		seen := map[string]*peerInfo{}
		for _, o := range list {
			if o.walkieID == c.walkieID {
				continue
			}
			ow := h.store.WalkieByID(o.walkieID)
			sc := sharedCodes(me, ow)
			if len(sc) == 0 {
				continue
			}
			p := seen[ow.ID]
			if p == nil {
				p = &peerInfo{Name: ow.Name, Codes: sc}
				seen[ow.ID] = p
			}
			p.Talking = p.Talking || o.talking.Load()
		}
		peers := []peerInfo{}
		for _, p := range seen {
			peers = append(peers, *p)
		}
		sort.Slice(peers, func(i, j int) bool { return peers[i].Name < peers[j].Name })
		b, _ := json.Marshal(map[string]any{"t": "state", "name": me.Name, "codes": me.Codes, "peers": peers})
		c.send(opText, b)
	}
}

func (h *Hub) Serve(ws *wsConn, walkieID string) {
	c := &client{walkieID: walkieID, ws: ws, out: make(chan outMsg, 128), done: make(chan struct{})}
	h.mu.Lock()
	h.clients[c] = true
	h.mu.Unlock()

	go func() { // writer
		ping := time.NewTicker(25 * time.Second)
		defer ping.Stop()
		for {
			select {
			case <-c.done:
				return
			case m := <-c.out:
				if ws.write(m.op, m.data) != nil {
					c.close()
					return
				}
			case <-ping.C:
				if ws.write(opPing, nil) != nil {
					c.close()
					return
				}
			}
		}
	}()

	h.broadcastState()
	defer func() {
		h.mu.Lock()
		delete(h.clients, c)
		h.mu.Unlock()
		if c.talking.Load() {
			h.relayTalk(c, false)
		}
		h.endCall(c, "gone")
		c.close()
		h.broadcastState()
	}()

	name := h.nameOf(walkieID)
	for {
		op, data, err := ws.Read()
		if err != nil {
			return
		}
		if op == opBinary {
			if len(data) == 0 || len(data) > 16000 {
				continue
			}
			nb := []byte(name)
			if len(nb) > 255 {
				nb = nb[:255]
			}
			frame := make([]byte, 0, 1+len(nb)+len(data))
			frame = append(frame, byte(len(nb)))
			frame = append(frame, nb...)
			frame = append(frame, data...)
			for _, p := range h.peersOf(walkieID) {
				p.send(opBinary, frame)
			}
			continue
		}
		var m struct {
			T     string `json:"t"`
			On    bool   `json:"on"`
			Text  string `json:"text"`
			Codes []Code `json:"codes"`
		}
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		switch m.T {
		case "talk":
			c.talking.Store(m.On)
			h.relayTalk(c, m.On)
		case "msg":
			t := strings.TrimSpace(m.Text)
			if t == "" {
				continue
			}
			if utf8.RuneCountInString(t) > 200 {
				t = string([]rune(t)[:200])
			}
			b, _ := json.Marshal(map[string]any{"t": "msg", "from": name, "text": t})
			for _, p := range h.peersOf(walkieID) {
				p.send(opText, b)
			}
		case "codes":
			h.store.mu.Lock()
			if w := h.store.WalkieByID(walkieID); w != nil {
				w.Codes = normCodes(m.Codes)
				h.store.saveLocked()
			}
			h.store.mu.Unlock()
			h.broadcastState()
		case "page":
			if time.Since(c.lastPage) < 4*time.Second {
				continue
			}
			c.lastPage = time.Now()
			b, _ := json.Marshal(map[string]any{"t": "page", "from": name})
			for _, p := range h.peersOf(walkieID) {
				p.send(opText, b)
			}
		case "vreq", "vans", "vsig", "vend":
			h.handleCall(c, name, m.T, data)
		case "hello":
			h.broadcastState()
		}
	}
}

func (h *Hub) relayTalk(c *client, on bool) {
	b, _ := json.Marshal(map[string]any{"t": "talk", "from": h.nameOf(c.walkieID), "on": on})
	for _, p := range h.peersOf(c.walkieID) {
		p.send(opText, b)
	}
}

// Kick disconnects all sessions of a walkie (e.g. after delete or password change).
func (h *Hub) Kick(walkieID string) {
	h.mu.RLock()
	var list []*client
	for c := range h.clients {
		if c.walkieID == walkieID {
			list = append(list, c)
		}
	}
	h.mu.RUnlock()
	for _, c := range list {
		c.close()
	}
}
