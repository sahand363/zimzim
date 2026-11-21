package main

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"sync"
	"time"
)

// Hidden video calls. Signaling rides the walkie WebSocket; media goes phone to phone
// (or through the TURN relay on this server when the networks don't allow direct).

type callState struct {
	mu      sync.Mutex
	partner map[*client]*client   // linked calls, both directions
	ringing map[*client][]*client // caller -> callee connections being rung
	ringBy  map[*client]*client   // callee connection -> caller
	tries   map[string][]time.Time
}

func newCallState() *callState {
	return &callState{partner: map[*client]*client{}, ringing: map[*client][]*client{}, ringBy: map[*client]*client{}, tries: map[string][]time.Time{}}
}

func sendJSON(c *client, v any) { b, _ := json.Marshal(v); c.send(opText, b) }

// allowTry limits secret guesses per walkie: 5 per 10 minutes.
func (cs *callState) allowTry(id string) bool {
	now := time.Now()
	var keep []time.Time
	for _, t := range cs.tries[id] {
		if now.Sub(t) < 10*time.Minute {
			keep = append(keep, t)
		}
	}
	cs.tries[id] = keep
	return len(keep) < 5
}

func (h *Hub) handleCall(c *client, name, typ string, raw []byte) {
	cs := h.calls
	var m struct {
		Secret string          `json:"secret"`
		OK     bool            `json:"ok"`
		Data   json.RawMessage `json:"data"`
	}
	json.Unmarshal(raw, &m)
	switch typ {
	case "vreq":
		h.store.mu.RLock()
		secret := h.store.cfg.VideoSecret
		h.store.mu.RUnlock()
		if secret == "" {
			secret = "4"
		}
		cs.mu.Lock()
		if !cs.allowTry(c.walkieID) {
			cs.mu.Unlock()
			sendJSON(c, map[string]any{"t": "vfail", "why": "wait"})
			return
		}
		if subtle.ConstantTimeCompare([]byte(m.Secret), []byte(secret)) != 1 {
			cs.tries[c.walkieID] = append(cs.tries[c.walkieID], time.Now())
			cs.mu.Unlock()
			sendJSON(c, map[string]any{"t": "vfail", "why": "secret"})
			return
		}
		stale := cs.partner[c] != nil || cs.ringing[c] != nil
		cs.mu.Unlock()
		if stale { // the caller's old call is clearly over: clean it up instead of saying busy
			h.endCall(c, "hangup")
		}
		busy := false
		if busy {
			sendJSON(c, map[string]any{"t": "vfail", "why": "busy"})
			return
		}
		// Exactly one other person on the channel.
		peers := h.peersOf(c.walkieID)
		ids := map[string]bool{}
		for _, p := range peers {
			ids[p.walkieID] = true
		}
		if len(ids) != 1 {
			sendJSON(c, map[string]any{"t": "vfail", "why": "count", "n": len(ids)})
			return
		}
		// Someone still "in a call" with this same walkie: that call already died, clear it.
		for _, p := range peers {
			cs.mu.Lock()
			q := cs.partner[p]
			cs.mu.Unlock()
			if q != nil && q.walkieID == c.walkieID {
				h.endCall(p, "hangup")
			}
		}
		cs.mu.Lock()
		var ring []*client
		for _, p := range peers {
			if cs.partner[p] == nil && cs.ringBy[p] == nil {
				ring = append(ring, p)
			}
		}
		if len(ring) == 0 {
			cs.mu.Unlock()
			sendJSON(c, map[string]any{"t": "vfail", "why": "busy"})
			return
		}
		cs.ringing[c] = ring
		for _, p := range ring {
			cs.ringBy[p] = c
		}
		cs.mu.Unlock()
		for _, p := range ring {
			sendJSON(p, map[string]any{"t": "vreq", "from": name})
		}
		sendJSON(c, map[string]any{"t": "vringing", "to": h.nameOf(ring[0].walkieID)})
		go func() { // stop ringing after 40 s
			time.Sleep(40 * time.Second)
			cs.mu.Lock()
			still := cs.ringing[c]
			cs.mu.Unlock()
			if still != nil {
				h.endRing(c, "noanswer")
			}
		}()
	case "vans":
		cs.mu.Lock()
		caller := cs.ringBy[c]
		if caller == nil {
			cs.mu.Unlock()
			return
		}
		others := cs.ringing[caller]
		delete(cs.ringing, caller)
		for _, p := range others {
			delete(cs.ringBy, p)
		}
		if m.OK {
			cs.partner[caller], cs.partner[c] = c, caller
		}
		cs.mu.Unlock()
		for _, p := range others {
			if p != c {
				sendJSON(p, map[string]any{"t": "vend", "why": "elsewhere"})
			}
		}
		if m.OK {
			sendJSON(caller, map[string]any{"t": "vaccept", "from": name})
		} else {
			sendJSON(caller, map[string]any{"t": "vend", "why": "declined"})
		}
	case "vsig":
		cs.mu.Lock()
		p := cs.partner[c]
		cs.mu.Unlock()
		if p != nil && len(m.Data) < 20000 {
			sendJSON(p, map[string]any{"t": "vsig", "data": m.Data})
		}
	case "vend":
		h.endCall(c, "hangup")
	}
}

func (h *Hub) endRing(caller *client, why string) {
	cs := h.calls
	cs.mu.Lock()
	ring := cs.ringing[caller]
	delete(cs.ringing, caller)
	for _, p := range ring {
		delete(cs.ringBy, p)
	}
	cs.mu.Unlock()
	for _, p := range ring {
		sendJSON(p, map[string]any{"t": "vend", "why": why})
	}
	sendJSON(caller, map[string]any{"t": "vend", "why": why})
}

// endCall cleans up any call or ringing this connection is part of.
func (h *Hub) endCall(c *client, why string) {
	cs := h.calls
	cs.mu.Lock()
	p := cs.partner[c]
	delete(cs.partner, c)
	if p != nil {
		delete(cs.partner, p)
	}
	_, isCaller := cs.ringing[c]
	caller := cs.ringBy[c]
	cs.mu.Unlock()
	if p != nil {
		sendJSON(p, map[string]any{"t": "vend", "why": why})
	}
	if isCaller {
		h.endRing(c, "cancelled")
	}
	if caller != nil { // callee hung up while ringing = decline
		cs.mu.Lock()
		if cs.ringBy[c] == caller {
			delete(cs.ringBy, c)
			rest := cs.ringing[caller][:0]
			for _, x := range cs.ringing[caller] {
				if x != c {
					rest = append(rest, x)
				}
			}
			if len(rest) == 0 {
				delete(cs.ringing, caller)
			} else {
				cs.ringing[caller] = rest
			}
			cs.mu.Unlock()
			if len(rest) == 0 {
				sendJSON(caller, map[string]any{"t": "vend", "why": "declined"})
			}
		} else {
			cs.mu.Unlock()
		}
	}
}

// iceServers returns STUN/TURN entries with short-lived TURN credentials (coturn use-auth-secret).
func iceServers(cfg Config, host, user string) []map[string]any {
	if cfg.TurnSecret == "" {
		return []map[string]any{}
	}
	if cfg.TurnHost != "" {
		host = cfg.TurnHost
	}
	port := cfg.TurnPort
	if port == 0 {
		port = 3478
	}
	hp := host + ":" + strconv.Itoa(port)
	u := strconv.FormatInt(time.Now().Add(12*time.Hour).Unix(), 10) + ":" + user
	mac := hmac.New(sha1.New, []byte(cfg.TurnSecret))
	mac.Write([]byte(u))
	cred := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return []map[string]any{
		{"urls": []string{"stun:" + hp}},
		{"urls": []string{"turn:" + hp + "?transport=udp", "turn:" + hp + "?transport=tcp"}, "username": u, "credential": cred},
	}
}
