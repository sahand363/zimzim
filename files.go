package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Files: send any file to everyone who shares an ON code with you. The server keeps it
// for them for 7 days (online or not), then deletes it.

const (
	fileMaxDefault = 50 // MB per file unless the admin changes it (menu: File size limit)
	fileMaxTop     = 2000
	fileKeep       = 7 * 24 * time.Hour
	filePerBox     = 30
)

// fileMaxBytes is the per-file limit from config (MB), 50 MB by default.
func (h *Hub) fileMaxBytes() int64 {
	h.store.mu.RLock()
	mb := h.store.cfg.FileMaxMB
	h.store.mu.RUnlock()
	if mb <= 0 {
		mb = fileMaxDefault
	}
	return int64(mb) << 20
}

// boxLimit: at most this much waiting for one walkie (500 MB, or 4 files' worth if the limit is big).
func boxLimit(max int64) int64 {
	if b := 4 * max; b > 500<<20 {
		return b
	}
	return 500 << 20
}

type fileMeta struct {
	ID   string `json:"id"`
	From string `json:"from"`
	Name string `json:"name"`
	Size int64  `json:"size"`
	At   int64  `json:"at"` // unix ms
	Read bool   `json:"read"`
}

type fileBox struct {
	mu  sync.Mutex
	dir string
}

func newFileBox(dir string) *fileBox {
	os.MkdirAll(dir, 0700)
	return &fileBox{dir: dir}
}

func (f *fileBox) box(w string) string { return filepath.Join(f.dir, w) }

func cleanFileName(n string) string {
	n = filepath.Base(strings.ReplaceAll(n, "\\", "/"))
	n = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 || strings.ContainsRune(`/\:*?"<>|`, r) {
			return '_'
		}
		return r
	}, n)
	n = strings.TrimSpace(n)
	for len(n) > 120 {
		_, sz := utf8.DecodeLastRuneInString(n)
		n = n[:len(n)-sz]
	}
	if n == "" || n == "." || n == ".." {
		n = "file"
	}
	return n
}

func (f *fileBox) list(w string) []fileMeta {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listLocked(w)
}

func (f *fileBox) listLocked(w string) []fileMeta {
	dir := f.box(w)
	ents, _ := os.ReadDir(dir)
	out := []fileMeta{}
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		var m fileMeta
		if err != nil || json.Unmarshal(b, &m) != nil || time.Since(time.UnixMilli(m.At)) > fileKeep {
			f.removeLocked(w, id)
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, id+".bin")); err != nil {
			f.removeLocked(w, id)
			continue
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At > out[j].At }) // newest first
	return out
}

func (f *fileBox) removeLocked(w, id string) {
	os.Remove(filepath.Join(f.box(w), id+".json"))
	os.Remove(filepath.Join(f.box(w), id+".bin"))
}

func (f *fileBox) remove(w, id string) {
	if validID(id) {
		f.mu.Lock()
		f.removeLocked(w, id)
		f.mu.Unlock()
	}
}

func (f *fileBox) removeAll(w string) { os.RemoveAll(f.box(w)) }

func (f *fileBox) sweep() {
	ents, _ := os.ReadDir(f.dir)
	for _, e := range ents {
		if e.IsDir() && e.Name() != "tmp" {
			f.list(e.Name())
		}
	}
	// leftovers of broken uploads
	tmps, _ := os.ReadDir(filepath.Join(f.dir, "tmp"))
	for _, t := range tmps {
		if i, err := t.Info(); err == nil && time.Since(i.ModTime()) > time.Hour {
			os.Remove(filepath.Join(f.dir, "tmp", t.Name()))
		}
	}
}

// add puts the uploaded temp file into w's box (hard link, or copy if that fails).
func (f *fileBox) add(w string, m fileMeta, tmp string, boxSize int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	dir := f.box(w)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	l := f.listLocked(w)
	var used int64
	for _, x := range l {
		used += x.Size
	}
	// make room: drop the oldest until it fits
	for len(l) > 0 && (len(l) >= filePerBox || used+m.Size > boxSize) {
		old := l[len(l)-1]
		f.removeLocked(w, old.ID)
		used -= old.Size
		l = l[:len(l)-1]
	}
	dst := filepath.Join(dir, m.ID+".bin")
	if err := os.Link(tmp, dst); err != nil {
		if err := copyFile(tmp, dst); err != nil {
			return err
		}
	}
	b, _ := json.Marshal(m)
	return os.WriteFile(filepath.Join(dir, m.ID+".json"), b, 0600)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}

func (f *fileBox) markRead(w, id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := filepath.Join(f.box(w), id+".json")
	b, err := os.ReadFile(p)
	var m fileMeta
	if err != nil || json.Unmarshal(b, &m) != nil || m.Read {
		return
	}
	m.Read = true
	b, _ = json.Marshal(m)
	os.WriteFile(p, b, 0600)
}

func (f *fileBox) get(w, id string) (fileMeta, string, bool) {
	if !validID(id) {
		return fileMeta{}, "", false
	}
	b, err := os.ReadFile(filepath.Join(f.box(w), id+".json"))
	var m fileMeta
	if err != nil || json.Unmarshal(b, &m) != nil {
		return m, "", false
	}
	return m, filepath.Join(f.box(w), id+".bin"), true
}

type fileTarget struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Online bool   `json:"online"`
}

// fileTargets: who this walkie can send a file to right now, with online state.
func (h *Hub) fileTargets(id string) []fileTarget {
	on := map[string]bool{}
	h.mu.RLock()
	for c := range h.clients {
		on[c.walkieID] = true
	}
	h.mu.RUnlock()
	var out []fileTarget
	for _, t := range h.sharedWith(id) {
		out = append(out, fileTarget{ID: t, Name: h.nameOf(t), Online: on[t]})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// sharedWith: every other walkie that shares an ON code with id (online or not).
func (h *Hub) sharedWith(id string) []string {
	h.store.mu.RLock()
	defer h.store.mu.RUnlock()
	me := h.store.WalkieByID(id)
	var out []string
	for _, w := range h.store.cfg.Walkies {
		if w.ID != id && len(sharedCodes(me, w)) > 0 {
			out = append(out, w.ID)
		}
	}
	return out
}

// tellWalkie sends a JSON message to every open connection of a walkie.
func (h *Hub) tellWalkie(id string, v any) {
	b, _ := json.Marshal(v)
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		if c.walkieID == id {
			c.send(opText, b)
		}
	}
}

// fileUpload handles POST api/files?name=… with the raw file as the body.
func (a *App) fileUpload(w http.ResponseWriter, r *http.Request, wk *Walkie) {
	h := a.hub
	// Only the walkies the sender picked, and only if they still share an ON code.
	pick := map[string]bool{}
	for _, t := range strings.Split(r.URL.Query().Get("to"), ",") {
		if t = strings.TrimSpace(t); t != "" {
			pick[t] = true
		}
	}
	var to []string
	for _, t := range h.sharedWith(wk.ID) {
		if pick[t] {
			to = append(to, t)
		}
	}
	if len(pick) == 0 {
		writeJSON(w, 400, map[string]string{"error": "Pick who gets it."})
		return
	}
	if len(to) == 0 {
		writeJSON(w, 400, map[string]string{"error": "They no longer share a code with you."})
		return
	}
	fileMax := h.fileMaxBytes()
	tooBig := fmt.Sprintf("Too big. Max %d MB.", fileMax>>20)
	if r.ContentLength > fileMax {
		writeJSON(w, 413, map[string]string{"error": tooBig})
		return
	}
	name := cleanFileName(r.URL.Query().Get("name"))
	tmpDir := filepath.Join(h.files.dir, "tmp")
	os.MkdirAll(tmpDir, 0700)
	tf, err := os.CreateTemp(tmpDir, "up-*")
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "Server can't save files."})
		return
	}
	tmp := tf.Name()
	defer os.Remove(tmp)
	n, err := io.Copy(tf, http.MaxBytesReader(w, r.Body, fileMax))
	tf.Close()
	if err != nil {
		writeJSON(w, 413, map[string]string{"error": "Upload failed or " + strings.ToLower(tooBig[:1]) + tooBig[1:]})
		return
	}
	if n == 0 {
		writeJSON(w, 400, map[string]string{"error": "Empty file."})
		return
	}
	m := fileMeta{ID: randHex(8), From: h.nameOf(wk.ID), Name: name, Size: n, At: time.Now().UnixMilli()}
	var names []string
	for _, id := range to {
		if h.files.add(id, m, tmp, boxLimit(fileMax)) == nil {
			names = append(names, h.nameOf(id))
			h.tellWalkie(id, map[string]any{"t": "filelist", "list": h.files.list(id), "new": m.Name, "from": m.From})
		}
	}
	writeJSON(w, 200, map[string]any{"to": names})
}

func (a *App) fileDownload(w http.ResponseWriter, r *http.Request, wk *Walkie, id string) {
	m, p, ok := a.hub.files.get(wk.ID, id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(p)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	a.hub.files.markRead(wk.ID, id)
	ascii := strings.Map(func(r rune) rune {
		if r > 126 || r < 32 || r == '"' || r == '\\' {
			return '_'
		}
		return r
	}, m.Name)
	// Always a download, never shown in the page: an .html or .svg can't run here.
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, ascii, url.PathEscape(m.Name)))
	http.ServeContent(w, r, "", time.UnixMilli(m.At), f)
}
