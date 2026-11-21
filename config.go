package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Code struct {
	Code string `json:"code"`
	On   bool   `json:"on"`
}

type Walkie struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Slug    string `json:"slug"`
	User    string `json:"user"`
	Hash    string `json:"hash"`
	Codes   []Code `json:"codes"`
	Created int64  `json:"created"`
}

type Config struct {
	Port        int       `json:"port"`
	Domain      string    `json:"domain"`
	Cert        string    `json:"cert"`
	Key         string    `json:"key"`
	AdminPath   string    `json:"admin_path"`
	AdminUser   string    `json:"admin_user"`
	AdminHash   string    `json:"admin_hash"`
	VideoSecret string    `json:"video_secret"`
	TurnSecret  string    `json:"turn_secret"`
	TurnPort    int       `json:"turn_port"`
	TurnHost    string    `json:"turn_host"`
	Walkies     []*Walkie `json:"walkies"`
}

type Store struct {
	mu   sync.RWMutex
	path string
	cfg  Config
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func randWord(n int) string {
	const a = "abcdefghjkmnpqrstuvwxyz23456789"
	b := make([]byte, n)
	rand.Read(b)
	for i := range b {
		b[i] = a[int(b[i])%len(a)]
	}
	return string(b)
}

const hashIter = 120000

func hashPass(p string) string {
	salt := randHex(16)
	return fmt.Sprintf("s256$%d$%s$%s", hashIter, salt, derive(p, salt, hashIter))
}

func derive(p, salt string, iter int) string {
	h := sha256.Sum256([]byte(salt + "|" + p))
	for i := 1; i < iter; i++ {
		h = sha256.Sum256(append(h[:], []byte(p)...))
	}
	return hex.EncodeToString(h[:])
}

func checkPass(p, stored string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != "s256" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 1 || iter > 10000000 {
		return false
	}
	got := derive(p, parts[2], iter)
	return subtle.ConstantTimeCompare([]byte(got), []byte(parts[3])) == 1
}

func LoadStore(path string) (*Store, error) {
	s := &Store{path: path}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &s.cfg); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if s.cfg.Walkies == nil {
		s.cfg.Walkies = []*Walkie{}
	}
	return s, nil
}

// saveLocked writes config atomically; caller holds mu.
func (s *Store) saveLocked() error {
	b, err := json.MarshalIndent(s.cfg, "", "  ")
	if err != nil {
		return err
	}
	os.MkdirAll(filepath.Dir(s.path), 0700)
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

func (s *Store) WalkieBySlug(slug string) *Walkie {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, w := range s.cfg.Walkies {
		if w.Slug == slug {
			return w
		}
	}
	return nil
}

func (s *Store) WalkieByID(id string) *Walkie {
	for _, w := range s.cfg.Walkies {
		if w.ID == id {
			return w
		}
	}
	return nil
}

func cleanCode(c string) string {
	c = strings.TrimSpace(c)
	if len([]rune(c)) > 32 {
		c = string([]rune(c)[:32])
	}
	return c
}

func normCodes(in []Code) []Code {
	out := []Code{}
	seen := map[string]bool{}
	for _, c := range in {
		c.Code = cleanCode(c.Code)
		if c.Code == "" || seen[strings.ToLower(c.Code)] {
			continue
		}
		seen[strings.ToLower(c.Code)] = true
		out = append(out, c)
		if len(out) >= 50 {
			break
		}
	}
	return out
}

func newWalkie(name, user, pass string, codes []Code) (*Walkie, error) {
	name = strings.TrimSpace(name)
	user = strings.TrimSpace(user)
	if name == "" {
		return nil, errors.New("name is required")
	}
	// Blank username and password: the person picks their own on first open.
	hash := ""
	if user != "" || pass != "" {
		if user == "" {
			return nil, errors.New("username is required (or leave both blank)")
		}
		if len(pass) < 6 {
			return nil, errors.New("password must be at least 6 characters")
		}
		hash = hashPass(pass)
	}
	return &Walkie{
		ID: randHex(6), Name: name, Slug: randWord(20), User: user, Hash: hash,
		Codes: normCodes(codes), Created: time.Now().Unix(),
	}, nil
}
