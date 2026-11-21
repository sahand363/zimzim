package main

// Minimal RFC 6455 WebSocket server (stdlib only).

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

const (
	opCont   = 0x0
	opText   = 0x1
	opBinary = 0x2
	opClose  = 0x8
	opPing   = 0x9
	opPong   = 0xA
)

const maxMsg = 256 * 1024

type wsConn struct {
	c  net.Conn
	br *bufio.Reader
	wm sync.Mutex
}

func headerHas(h http.Header, key, token string) bool {
	for _, v := range h.Values(key) {
		for _, p := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(p), token) {
				return true
			}
		}
	}
	return false
}

func wsUpgrade(w http.ResponseWriter, r *http.Request) (*wsConn, error) {
	if !headerHas(r.Header, "Connection", "upgrade") || !headerHas(r.Header, "Upgrade", "websocket") {
		http.Error(w, "websocket only", http.StatusBadRequest)
		return nil, errors.New("not websocket")
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		http.Error(w, "bad key", http.StatusBadRequest)
		return nil, errors.New("no key")
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "no hijack", http.StatusInternalServerError)
		return nil, errors.New("no hijack")
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		return nil, err
	}
	h := sha1.Sum([]byte(key + wsGUID))
	accept := base64.StdEncoding.EncodeToString(h[:])
	resp := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + accept + "\r\n\r\n"
	conn.SetDeadline(time.Time{})
	if _, err := conn.Write([]byte(resp)); err != nil {
		conn.Close()
		return nil, err
	}
	return &wsConn{c: conn, br: brw.Reader}, nil
}

// Read returns one full message (text or binary).
func (ws *wsConn) Read() (int, []byte, error) {
	var msg []byte
	var msgOp int = -1
	for {
		ws.c.SetReadDeadline(time.Now().Add(75 * time.Second))
		var hdr [2]byte
		if _, err := io.ReadFull(ws.br, hdr[:]); err != nil {
			return 0, nil, err
		}
		fin := hdr[0]&0x80 != 0
		op := int(hdr[0] & 0x0F)
		masked := hdr[1]&0x80 != 0
		n := uint64(hdr[1] & 0x7F)
		switch n {
		case 126:
			var b [2]byte
			if _, err := io.ReadFull(ws.br, b[:]); err != nil {
				return 0, nil, err
			}
			n = uint64(binary.BigEndian.Uint16(b[:]))
		case 127:
			var b [8]byte
			if _, err := io.ReadFull(ws.br, b[:]); err != nil {
				return 0, nil, err
			}
			n = binary.BigEndian.Uint64(b[:])
		}
		if n > maxMsg || uint64(len(msg))+n > maxMsg {
			return 0, nil, errors.New("message too large")
		}
		var mask [4]byte
		if masked {
			if _, err := io.ReadFull(ws.br, mask[:]); err != nil {
				return 0, nil, err
			}
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(ws.br, payload); err != nil {
			return 0, nil, err
		}
		if masked {
			for i := range payload {
				payload[i] ^= mask[i%4]
			}
		}
		switch op {
		case opPing:
			ws.write(opPong, payload)
			continue
		case opPong:
			continue
		case opClose:
			ws.write(opClose, nil)
			return 0, nil, io.EOF
		case opText, opBinary:
			msgOp = op
			msg = payload
		case opCont:
			if msgOp < 0 {
				return 0, nil, errors.New("bad continuation")
			}
			msg = append(msg, payload...)
		default:
			return 0, nil, errors.New("bad opcode")
		}
		if fin {
			return msgOp, msg, nil
		}
	}
}

func (ws *wsConn) write(op int, data []byte) error {
	ws.wm.Lock()
	defer ws.wm.Unlock()
	hdr := make([]byte, 0, 10)
	hdr = append(hdr, 0x80|byte(op))
	n := len(data)
	switch {
	case n < 126:
		hdr = append(hdr, byte(n))
	case n < 65536:
		hdr = append(hdr, 126, byte(n>>8), byte(n))
	default:
		hdr = append(hdr, 127)
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(n))
		hdr = append(hdr, b[:]...)
	}
	ws.c.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if _, err := ws.c.Write(append(hdr, data...)); err != nil {
		return err
	}
	return nil
}

func (ws *wsConn) Close() { ws.c.Close() }
