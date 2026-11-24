package main

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Voice messages: when you talk and someone on your channel is offline, the server keeps
// what you said for them and gives it to them the next time they turn their walkie on.

const (
	vmMaxLen   = 60 * time.Second // one message is at most a minute of speech
	vmGap      = 1500 * time.Millisecond
	vmMinVoice = 600 * time.Millisecond
	vmKeep     = 7 * 24 * time.Hour
	vmPerBox   = 30
)

type vmMeta struct {
	ID   string `json:"id"`
	From string `json:"from"`
	At   int64  `json:"at"` // unix ms
	Ms   int    `json:"ms"`
	Read bool   `json:"read"`
}

type vmRec struct {
	to         []string // walkie IDs that were offline when it started
	frames     [][]byte
	start      time.Time
	lastVoiced time.Time
	voicedMs   int
	totalMs    int
}

type vmail struct {
	mu   sync.Mutex
	dir  string
	recs map[*client]*vmRec
}

func newVmail(dir string) *vmail {
	os.MkdirAll(dir, 0700)
	return &vmail{dir: dir, recs: map[*client]*vmRec{}}
}

// frameMs: byte 0 of a frame = codec (low bits) | 0x80 when it has voice.
func frameMs(f []byte) int {
	if len(f) < 2 {
		return 0
	}
	if f[0]&0x7f == 1 { // opus, 20 ms frames
		return 20
	}
	return (len(f) - 1) * 1000 / 32000 // pcm16 mono 16 kHz
}

// offlineOf: walkies sharing an ON code with id that have no open connection.
func (h *Hub) offlineOf(id string) []string {
	h.store.mu.RLock()
	defer h.store.mu.RUnlock()
	me := h.store.WalkieByID(id)
	h.mu.RLock()
	online := map[string]bool{}
	for c := range h.clients {
		online[c.walkieID] = true
	}
	h.mu.RUnlock()
	var out []string
	for _, w := range h.store.cfg.Walkies {
		if w.ID != id && !online[w.ID] && len(sharedCodes(me, w)) > 0 {
			out = append(out, w.ID)
		}
	}
	return out
}

// vmFeed is called with every audio frame a walkie sends.
func (h *Hub) vmFeed(c *client, f []byte) {
	if len(f) < 2 {
		return
	}
	voiced := f[0]&0x80 != 0
	now := time.Now()
	v := h.vm
	v.mu.Lock()
	r := v.recs[c]
	v.mu.Unlock()
	if r == nil {
		if !voiced {
			return
		}
		to := h.offlineOf(c.walkieID)
		if len(to) == 0 {
			return
		}
		r = &vmRec{to: to, start: now, lastVoiced: now}
		v.mu.Lock()
		v.recs[c] = r
		v.mu.Unlock()
	}
	if !voiced && now.Sub(r.lastVoiced) > vmGap {
		h.vmFlush(c)
		return
	}
	ms := frameMs(f)
	cp := append([]byte(nil), f...)
	v.mu.Lock()
	r.frames = append(r.frames, cp)
	r.totalMs += ms
	if voiced {
		r.voicedMs += ms
		r.lastVoiced = now
	}
	full := now.Sub(r.start) > vmMaxLen
	v.mu.Unlock()
	if full {
		h.vmFlush(c)
	}
}

// vmFlush finishes the message being recorded from c (if any) and saves it.
func (h *Hub) vmFlush(c *client) {
	v := h.vm
	v.mu.Lock()
	r := v.recs[c]
	delete(v.recs, c)
	v.mu.Unlock()
	if r == nil || time.Duration(r.voicedMs)*time.Millisecond < vmMinVoice {
		return
	}
	// trim trailing silence
	for len(r.frames) > 0 && r.frames[len(r.frames)-1][0]&0x80 == 0 {
		r.totalMs -= frameMs(r.frames[len(r.frames)-1])
		r.frames = r.frames[:len(r.frames)-1]
	}
	from := h.nameOf(c.walkieID)
	meta := vmMeta{ID: randHex(8), From: from, At: r.start.UnixMilli(), Ms: r.totalMs}
	var names []string
	for _, to := range r.to {
		if v.save(to, meta, r.frames) == nil {
			names = append(names, h.nameOf(to))
		}
	}
	if len(names) > 0 {
		sendJSON(c, map[string]any{"t": "vmsaved", "to": names})
	}
}

func (v *vmail) box(walkieID string) string { return filepath.Join(v.dir, walkieID) }

func (v *vmail) save(to string, m vmMeta, frames [][]byte) error {
	dir := v.box(to)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(dir, m.ID+".zvm"))
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	hdr, _ := json.Marshal(m)
	w.Write(hdr)
	w.WriteByte('\n')
	var l [2]byte
	for _, fr := range frames {
		binary.BigEndian.PutUint16(l[:], uint16(len(fr)))
		w.Write(l[:])
		w.Write(fr)
	}
	w.Flush()
	f.Close()
	v.prune(to)
	return nil
}

// list returns a walkie's waiting messages, oldest first, dropping expired ones.
func (v *vmail) list(walkieID string) []vmMeta {
	ents, _ := os.ReadDir(v.box(walkieID))
	out := []vmMeta{}
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".zvm") {
			continue
		}
		p := filepath.Join(v.box(walkieID), e.Name())
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		line, _ := bufio.NewReader(f).ReadBytes('\n')
		f.Close()
		var m vmMeta
		if json.Unmarshal(line, &m) != nil || time.Since(time.UnixMilli(m.At)) > vmKeep {
			os.Remove(p)
			os.Remove(strings.TrimSuffix(p, ".zvm") + ".read")
			continue
		}
		if _, err := os.Stat(strings.TrimSuffix(p, ".zvm") + ".read"); err == nil {
			m.Read = true
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At < out[j].At })
	return out
}

func (v *vmail) prune(walkieID string) {
	l := v.list(walkieID)
	for len(l) > vmPerBox {
		v.remove(walkieID, l[0].ID)
		l = l[1:]
	}
}

func validID(id string) bool {
	if len(id) != 16 {
		return false
	}
	for _, r := range id {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

// open returns the frames part of a message ([u16 len][frame]...), after the header line.
func (v *vmail) open(walkieID, id string) ([]byte, bool) {
	if !validID(id) {
		return nil, false
	}
	b, err := os.ReadFile(filepath.Join(v.box(walkieID), id+".zvm"))
	if err != nil {
		return nil, false
	}
	i := strings.IndexByte(string(b[:min(len(b), 512)]), '\n')
	if i < 0 {
		return nil, false
	}
	return b[i+1:], true
}

func (v *vmail) remove(walkieID, id string) {
	if validID(id) {
		os.Remove(filepath.Join(v.box(walkieID), id+".zvm"))
		os.Remove(filepath.Join(v.box(walkieID), id+".read"))
	}
}

// markRead keeps the message (until it expires) but stops counting it as new.
func (v *vmail) markRead(walkieID, id string) {
	if validID(id) {
		if _, err := os.Stat(filepath.Join(v.box(walkieID), id+".zvm")); err == nil {
			os.WriteFile(filepath.Join(v.box(walkieID), id+".read"), nil, 0600)
		}
	}
}

// sweep drops expired messages in every box, so disk space comes back even if nobody looks.
func (v *vmail) sweep() {
	ents, _ := os.ReadDir(v.dir)
	for _, e := range ents {
		if e.IsDir() {
			v.list(e.Name())
		}
	}
}

func (v *vmail) removeAll(walkieID string) { os.RemoveAll(v.box(walkieID)) }

