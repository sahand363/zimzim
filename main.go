package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

var version = "dev"

//go:embed web
var webFS embed.FS

const defaultConfig = "/etc/zimzim/config.json"

func main() {
	cfgPath := os.Getenv("ZIMZIM_CONFIG")
	if cfgPath == "" {
		cfgPath = defaultConfig
	}
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "serve":
		serve(cfgPath)
	case "init":
		fl := flag.NewFlagSet("init", flag.ExitOnError)
		port := fl.Int("port", 2096, "web port")
		fl.Parse(args)
		cmdInit(cfgPath, *port)
	case "set-admin":
		if len(args) != 2 {
			die("usage: set-admin USER PASS")
		}
		mutate(cfgPath, func(c *Config) error {
			if strings.TrimSpace(args[0]) == "" || len(args[1]) < 6 {
				return fmt.Errorf("username required, password at least 6 characters")
			}
			c.AdminUser = strings.TrimSpace(args[0])
			c.AdminHash = hashPass(args[1])
			return nil
		})
		fmt.Println("Admin login updated.")
	case "set-port":
		if len(args) != 1 {
			die("usage: set-port PORT")
		}
		p, err := strconv.Atoi(args[0])
		if err != nil || p < 1 || p > 65535 {
			die("invalid port")
		}
		mutate(cfgPath, func(c *Config) error { c.Port = p; return nil })
		fmt.Println("Port set to", p)
	case "set-tls":
		if len(args) != 3 {
			die("usage: set-tls DOMAIN CERTFILE KEYFILE")
		}
		mutate(cfgPath, func(c *Config) error { c.Domain, c.Cert, c.Key = args[0], args[1], args[2]; return nil })
		fmt.Println("TLS enabled for", args[0])
	case "clear-tls":
		mutate(cfgPath, func(c *Config) error { c.Domain, c.Cert, c.Key = "", "", ""; return nil })
		fmt.Println("TLS disabled.")
	case "new-admin-path":
		mutate(cfgPath, func(c *Config) error { c.AdminPath = randWord(12); return nil })
		fmt.Println("Admin link changed.")
	case "info":
		s, err := LoadStore(cfgPath)
		if err != nil {
			die(err.Error())
		}
		c := s.cfg
		fmt.Printf("url=%s\nuser=%s\nport=%d\ndomain=%s\ntls=%v\nwalkies=%d\n", adminURL(c, ""), c.AdminUser, c.Port, c.Domain, c.Cert != "", len(c.Walkies))
	case "url":
		s, err := LoadStore(cfgPath)
		if err != nil {
			die(err.Error())
		}
		host := ""
		if len(args) > 0 {
			host = args[0]
		}
		fmt.Println(adminURL(s.cfg, host))
	case "version":
		fmt.Println(version)
	default:
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Println("zimzim " + version + "\ncommands: serve | init [-port N] | set-admin USER PASS | set-port N | set-tls DOMAIN CERT KEY | clear-tls | new-admin-path | info | url [HOST] | version")
}

func die(m string) { fmt.Fprintln(os.Stderr, m); os.Exit(1) }

func mutate(path string, f func(*Config) error) {
	s, err := LoadStore(path)
	if err != nil {
		die(err.Error())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := f(&s.cfg); err != nil {
		die(err.Error())
	}
	if err := s.saveLocked(); err != nil {
		die(err.Error())
	}
}

func cmdInit(path string, port int) {
	if _, err := os.Stat(path); err == nil {
		mutate(path, func(c *Config) error { c.Port = port; return nil })
		fmt.Println("existing config kept; port set to", port)
		return
	}
	pass := randWord(12)
	s := &Store{path: path, cfg: Config{Port: port, AdminPath: randWord(12), AdminUser: "admin", AdminHash: hashPass(pass), Walkies: []*Walkie{}}}
	if err := s.Save(); err != nil {
		die(err.Error())
	}
	fmt.Printf("user=admin\npass=%s\n", pass)
}

func adminURL(c Config, host string) string {
	scheme := "http"
	if c.Cert != "" {
		scheme = "https"
	}
	if c.Domain != "" {
		host = c.Domain
	}
	if host == "" {
		host = "SERVER_IP"
	}
	port := ":" + strconv.Itoa(c.Port)
	if (scheme == "https" && c.Port == 443) || (scheme == "http" && c.Port == 80) {
		port = ""
	}
	return fmt.Sprintf("%s://%s%s/%s/", scheme, host, port, c.AdminPath)
}

// ---------- sessions & rate limiting ----------

type sessions struct {
	mu sync.Mutex
	m  map[string]sess
}
type sess struct {
	who string // "admin" or walkie id
	exp time.Time
}

func (s *sessions) new(who string) string {
	t := randHex(24)
	s.mu.Lock()
	s.m[t] = sess{who, time.Now().Add(30 * 24 * time.Hour)}
	s.mu.Unlock()
	return t
}
func (s *sessions) get(t string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[t]
	if !ok || time.Now().After(v.exp) {
		delete(s.m, t)
		return ""
	}
	return v.who
}
func (s *sessions) drop(t string) { s.mu.Lock(); delete(s.m, t); s.mu.Unlock() }
func (s *sessions) dropWho(who string) {
	s.mu.Lock()
	for k, v := range s.m {
		if v.who == who {
			delete(s.m, k)
		}
	}
	s.mu.Unlock()
}

type limiter struct {
	mu sync.Mutex
	m  map[string][]time.Time
}

func (l *limiter) blocked(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cut := time.Now().Add(-10 * time.Minute)
	var keep []time.Time
	for _, t := range l.m[ip] {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	l.m[ip] = keep
	return len(keep) >= 8
}
func (l *limiter) fail(ip string) { l.mu.Lock(); l.m[ip] = append(l.m[ip], time.Now()); l.mu.Unlock() }

func clientIP(r *http.Request) string {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

// ---------- server ----------

type App struct {
	store *Store
	hub   *Hub
	sess  *sessions
	lim   *limiter
	tls   bool
	files fs.FS
}

func serve(path string) {
	s, err := LoadStore(path)
	if err != nil {
		log.Fatal(err)
	}
	sub, _ := fs.Sub(webFS, "web")
	a := &App{store: s, hub: NewHub(s), sess: &sessions{m: map[string]sess{}}, lim: &limiter{m: map[string][]time.Time{}}, files: sub}
	a.tls = s.cfg.Cert != "" && s.cfg.Key != ""
	srv := &http.Server{
		Addr:              ":" + strconv.Itoa(s.cfg.Port),
		Handler:           a,
		ReadHeaderTimeout: 15 * time.Second,
	}
	log.Printf("zimzim %s listening on %s (tls=%v)", version, srv.Addr, a.tls)
	if a.tls {
		log.Fatal(srv.ListenAndServeTLS(s.cfg.Cert, s.cfg.Key))
	}
	log.Fatal(srv.ListenAndServe())
}

func (a *App) setCookie(w http.ResponseWriter, name, val, path string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: val, Path: path, MaxAge: maxAge, HttpOnly: true, Secure: a.tls, SameSite: http.SameSiteStrictMode})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func (a *App) serveFile(w http.ResponseWriter, name, ctype string) {
	b, err := fs.ReadFile(a.files, name)
	if err != nil {
		http.NotFound(w, nil)
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(b)
}

func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	p := r.URL.Path
	a.store.mu.RLock()
	adminPath := "/" + a.store.cfg.AdminPath + "/"
	a.store.mu.RUnlock()
	switch {
	case p == "/icon.svg":
		w.Header().Set("Cache-Control", "max-age=86400")
		a.serveFile(w, "icon.svg", "image/svg+xml")
	case strings.HasPrefix(p, adminPath):
		a.admin(w, r, adminPath, strings.TrimPrefix(p, adminPath))
	case p == strings.TrimSuffix(adminPath, "/"):
		http.Redirect(w, r, adminPath, http.StatusFound)
	case strings.HasPrefix(p, "/w/"):
		rest := strings.TrimPrefix(p, "/w/")
		slug, sub, _ := strings.Cut(rest, "/")
		wk := a.store.WalkieBySlug(slug)
		if wk == nil || slug == "" {
			http.NotFound(w, r)
			return
		}
		if !strings.Contains(rest, "/") {
			http.Redirect(w, r, "/w/"+slug+"/", http.StatusFound)
			return
		}
		a.walkie(w, r, wk, slug, sub)
	default:
		http.NotFound(w, r)
	}
}

// ---------- walkie endpoints ----------

func (a *App) walkie(w http.ResponseWriter, r *http.Request, wk *Walkie, slug, sub string) {
	cookie := "zz_" + slug[:8]
	base := "/w/" + slug + "/"
	authed := func() bool {
		c, err := r.Cookie(cookie)
		return err == nil && a.sess.get(c.Value) == wk.ID
	}
	switch sub {
	case "":
		a.serveFile(w, "walkie.html", "text/html; charset=utf-8")
	case "manifest.webmanifest":
		a.store.mu.RLock()
		name := wk.Name
		a.store.mu.RUnlock()
		w.Header().Set("Content-Type", "application/manifest+json")
		json.NewEncoder(w).Encode(map[string]any{
			"name": "ZimZim · " + name, "short_name": "ZimZim", "start_url": base, "scope": base,
			"display": "standalone", "background_color": "#101211", "theme_color": "#101211", "orientation": "portrait",
			"icons": []map[string]string{{"src": "/icon.svg", "sizes": "any", "type": "image/svg+xml", "purpose": "any"}},
		})
	case "api/me":
		if !authed() {
			writeJSON(w, 401, map[string]string{"error": "login"})
			return
		}
		a.store.mu.RLock()
		writeJSON(w, 200, map[string]any{"name": wk.Name, "codes": wk.Codes})
		a.store.mu.RUnlock()
	case "api/login":
		if r.Method != http.MethodPost {
			http.Error(w, "method", 405)
			return
		}
		ip := clientIP(r)
		if a.lim.blocked(ip) {
			writeJSON(w, 429, map[string]string{"error": "Too many tries. Wait 10 minutes."})
			return
		}
		var in struct{ User, Pass string }
		json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in)
		a.store.mu.RLock()
		ok := in.User == wk.User && checkPass(in.Pass, wk.Hash)
		a.store.mu.RUnlock()
		if !ok {
			a.lim.fail(ip)
			writeJSON(w, 401, map[string]string{"error": "Wrong username or password."})
			return
		}
		a.setCookie(w, cookie, a.sess.new(wk.ID), base, 30*24*3600)
		writeJSON(w, 200, map[string]string{"ok": "1"})
	case "api/logout":
		if c, err := r.Cookie(cookie); err == nil {
			a.sess.drop(c.Value)
		}
		a.setCookie(w, cookie, "", base, -1)
		writeJSON(w, 200, map[string]string{"ok": "1"})
	case "ws":
		if !authed() {
			http.Error(w, "login", 401)
			return
		}
		ws, err := wsUpgrade(w, r)
		if err != nil {
			return
		}
		a.hub.Serve(ws, wk.ID)
	default:
		http.NotFound(w, r)
	}
}

// ---------- admin endpoints ----------

type walkieView struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	User  string `json:"user"`
	Link  string `json:"link"`
	Codes []Code `json:"codes"`
}

func (a *App) admin(w http.ResponseWriter, r *http.Request, base, sub string) {
	cookie := "zz_admin"
	authed := func() bool {
		c, err := r.Cookie(cookie)
		return err == nil && a.sess.get(c.Value) == "admin"
	}
	switch sub {
	case "":
		a.serveFile(w, "admin.html", "text/html; charset=utf-8")
		return
	case "api/login":
		if r.Method != http.MethodPost {
			http.Error(w, "method", 405)
			return
		}
		ip := clientIP(r)
		if a.lim.blocked(ip) {
			writeJSON(w, 429, map[string]string{"error": "Too many tries. Wait 10 minutes."})
			return
		}
		var in struct{ User, Pass string }
		json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in)
		a.store.mu.RLock()
		ok := in.User == a.store.cfg.AdminUser && checkPass(in.Pass, a.store.cfg.AdminHash)
		a.store.mu.RUnlock()
		if !ok {
			a.lim.fail(ip)
			writeJSON(w, 401, map[string]string{"error": "Wrong username or password."})
			return
		}
		a.setCookie(w, cookie, a.sess.new("admin"), base, 7*24*3600)
		writeJSON(w, 200, map[string]string{"ok": "1"})
		return
	case "api/logout":
		if c, err := r.Cookie(cookie); err == nil {
			a.sess.drop(c.Value)
		}
		a.setCookie(w, cookie, "", base, -1)
		writeJSON(w, 200, map[string]string{"ok": "1"})
		return
	}
	if !authed() {
		writeJSON(w, 401, map[string]string{"error": "login"})
		return
	}
	scheme := "http"
	if a.tls {
		scheme = "https"
	}
	origin := scheme + "://" + r.Host
	list := func() []walkieView {
		out := []walkieView{}
		for _, wk := range a.store.cfg.Walkies {
			out = append(out, walkieView{wk.ID, wk.Name, wk.User, origin + "/w/" + wk.Slug + "/", wk.Codes})
		}
		return out
	}
	var in struct {
		Name, User, Pass string
		Codes            []Code
		NewLink          bool
	}
	if r.Method == http.MethodPost || r.Method == http.MethodPut {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536)).Decode(&in); err != nil {
			writeJSON(w, 400, map[string]string{"error": "bad request"})
			return
		}
	}
	if sub == "api/walkies" {
		switch r.Method {
		case http.MethodGet:
			a.store.mu.RLock()
			writeJSON(w, 200, list())
			a.store.mu.RUnlock()
		case http.MethodPost:
			wk, err := newWalkie(in.Name, in.User, in.Pass, in.Codes)
			if err != nil {
				writeJSON(w, 400, map[string]string{"error": err.Error()})
				return
			}
			a.store.mu.Lock()
			a.store.cfg.Walkies = append(a.store.cfg.Walkies, wk)
			a.store.saveLocked()
			writeJSON(w, 200, list())
			a.store.mu.Unlock()
		default:
			http.Error(w, "method", 405)
		}
		return
	}
	if id, ok := strings.CutPrefix(sub, "api/walkies/"); ok {
		a.store.mu.Lock()
		wk := a.store.WalkieByID(id)
		if wk == nil {
			a.store.mu.Unlock()
			writeJSON(w, 404, map[string]string{"error": "not found"})
			return
		}
		kick := false
		switch r.Method {
		case http.MethodPut:
			if n := strings.TrimSpace(in.Name); n != "" {
				wk.Name = n
			}
			if u := strings.TrimSpace(in.User); u != "" && u != wk.User {
				wk.User, kick = u, true
			}
			if in.Pass != "" {
				if len(in.Pass) < 6 {
					a.store.mu.Unlock()
					writeJSON(w, 400, map[string]string{"error": "password must be at least 6 characters"})
					return
				}
				wk.Hash, kick = hashPass(in.Pass), true
			}
			if in.Codes != nil {
				wk.Codes = normCodes(in.Codes)
			}
			if in.NewLink {
				wk.Slug, kick = randWord(20), true
			}
		case http.MethodDelete:
			ws := a.store.cfg.Walkies[:0]
			for _, x := range a.store.cfg.Walkies {
				if x.ID != id {
					ws = append(ws, x)
				}
			}
			a.store.cfg.Walkies = ws
			kick = true
		default:
			a.store.mu.Unlock()
			http.Error(w, "method", 405)
			return
		}
		a.store.saveLocked()
		out := list()
		a.store.mu.Unlock()
		if kick {
			a.sess.dropWho(id)
			a.hub.Kick(id)
		}
		a.hub.broadcastState()
		writeJSON(w, 200, out)
		return
	}
	http.NotFound(w, r)
}
