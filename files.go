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
	fileMax     = 50 << 20 // 50 MB per file
	fileKeep    = 7 * 24 * time.Hour
	filePerBox  = 30
	fileBoxSize = 500 << 20 // at most 500 MB waiting for one walkie
)

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
func (f *fileBox) add(w string, m fileMeta, tmp string) error {
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
	for len(l) > 0 && (len(l) >= filePerBox || used+m.Size > fileBoxSize) {
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
	to := h.sharedWith(wk.ID)
	if len(to) == 0 {
		writeJSON(w, 400, map[string]string{"error": "Nobody shares a code with you."})
		return
	}
	if r.ContentLength > fileMax {
		writeJSON(w, 413, map[string]string{"error": "Too big. Max 50 MB."})
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
		writeJSON(w, 413, map[string]string{"error": "Upload failed or file over 50 MB."})
		return
	}
	if n == 0 {
		writeJSON(w, 400, map[string]string{"error": "Empty file."})
		return
	}
	m := fileMeta{ID: randHex(8), From: h.nameOf(wk.ID), Name: name, Size: n, At: time.Now().UnixMilli()}
	var names []string
	for _, id := range to {
		if h.files.add(id, m, tmp) == nil {
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
