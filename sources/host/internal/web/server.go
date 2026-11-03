// Package web implements the loopback control API, one-time bootstrap and SSE.
package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/jjundot/EspKVM/internal/devices"
	"github.com/jjundot/EspKVM/internal/switcher"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type PendingView struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	IP    string `json:"ip"`
	Port  uint16 `json:"port"`
	State string `json:"state"`
}
type State struct {
	Input   string         `json:"input"`
	Version string         `json:"version"`
	Error   string         `json:"error,omitempty"`
	Devices []devices.View `json:"devices"`
	Switch  switcher.View  `json:"switch"`
	Pending []PendingView  `json:"pending"`
	Hotkey  []uint16       `json:"hotkey"`
}
type Operation func(context.Context, string, json.RawMessage) (any, error)
type PasswordAuth interface {
	VerifyWebPassword(string) bool
	ChangeWebPassword(current, next string) error
}
type defaultPasswordAuth struct{}

func (defaultPasswordAuth) VerifyWebPassword(password string) bool { return password == "admin" }
func (defaultPasswordAuth) ChangeWebPassword(string, string) error {
	return errors.New("password change is unavailable")
}

type authorization struct {
	csrf    string
	expires time.Time
}
type Server struct {
	host, origin, bootstrap string
	expires                 time.Time
	mu                      sync.Mutex
	sessions                map[string]authorization
	state                   func() State
	operate                 Operation
	auth                    PasswordAuth
	assets                  fs.FS
	streams                 chan struct{}
	sequence                atomic.Uint64
}

func random() (string, error) {
	var b [32]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}
func New(host string, state func() State, operate Operation, assets fs.FS) (*Server, string, error) {
	return NewWithAuth(host, state, operate, assets, defaultPasswordAuth{})
}
func NewWithAuth(host string, state func() State, operate Operation, assets fs.FS, auth PasswordAuth) (*Server, string, error) {
	if host != "127.0.0.1:62626" {
		return nil, "", errors.New("control UI must bind 127.0.0.1:62626")
	}
	token, e := random()
	if e != nil {
		return nil, "", e
	}
	if auth == nil {
		return nil, "", errors.New("web password authentication is required")
	}
	s := &Server{host: host, origin: "http://" + host, bootstrap: token, expires: time.Now().Add(2 * time.Minute), sessions: map[string]authorization{}, state: state, operate: operate, auth: auth, assets: assets, streams: make(chan struct{}, 8)}
	return s, token, nil
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/bootstrap", s.exchange)
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"version": s.state().Version})
	})
	mux.HandleFunc("POST /api/password", s.authorized(s.changePassword))
	mux.HandleFunc("GET /api/session", s.authorized(func(w http.ResponseWriter, r *http.Request, a authorization) {
		writeJSON(w, map[string]string{"csrf": a.csrf})
	}))
	mux.HandleFunc("GET /api/state", s.authorized(func(w http.ResponseWriter, r *http.Request, a authorization) { writeJSON(w, s.state()) }))
	mux.HandleFunc("GET /api/events", s.authorized(s.events))
	for _, op := range []string{"target", "recording", "device-save", "device-delete", "device-order", "takeover", "hotkey", "pending-discard", "pending-verify", "serial-ports", "serial-info", "serial-set", "device-config-get", "device-config-set"} {
		op := op
		mux.HandleFunc("POST /api/"+op, s.authorized(func(w http.ResponseWriter, r *http.Request, a authorization) {
			if !s.writeAllowed(r) || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(a.csrf)) != 1 {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			body, e := readBody(w, r)
			if e != nil {
				http.Error(w, "invalid JSON request", http.StatusBadRequest)
				return
			}
			result, e := s.operate(r.Context(), op, body)
			if e != nil {
				http.Error(w, e.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, result)
		}))
	}
	if s.assets != nil {
		mux.Handle("GET /", http.FileServerFS(s.assets))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'; object-src 'none'; base-uri 'none'; form-action 'self'")
		if r.Host != s.host || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != s.origin) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) issueSession(w http.ResponseWriter) error {
	cookie, e := random()
	if e != nil {
		return e
	}
	csrf, e := random()
	if e != nil {
		return e
	}
	s.sessions[cookie] = authorization{csrf: csrf, expires: time.Now().Add(8 * time.Hour)}
	http.SetCookie(w, &http.Cookie{Name: "espkvm", Value: cookie, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 8 * 60 * 60})
	writeJSON(w, map[string]string{"csrf": csrf})
	return nil
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.writeAllowed(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	body, e := readBody(w, r)
	var req struct {
		Password string `json:"password"`
	}
	if e != nil || Decode(body, &req) != nil || len(req.Password) == 0 || len(req.Password) > 128 {
		http.Error(w, "invalid login request", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.auth.VerifyWebPassword(req.Password) {
		http.Error(w, "incorrect password", http.StatusUnauthorized)
		return
	}
	if e = s.issueSession(w); e != nil {
		http.Error(w, "entropy unavailable", http.StatusInternalServerError)
	}
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request, a authorization) {
	if !s.writeAllowed(r) || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(a.csrf)) != 1 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	body, e := readBody(w, r)
	var req struct {
		Current string `json:"current"`
		Next    string `json:"next"`
	}
	if e != nil || Decode(body, &req) != nil || len(req.Current) == 0 || len(req.Current) > 128 || len(req.Next) > 128 {
		http.Error(w, "invalid password request", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e = s.auth.ChangeWebPassword(req.Current, req.Next); e != nil {
		http.Error(w, e.Error(), http.StatusBadRequest)
		return
	}
	s.sessions = map[string]authorization{}
	http.SetCookie(w, &http.Cookie{Name: "espkvm", Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) writeAllowed(r *http.Request) bool {
	media, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return e == nil && media == "application/json" && r.Header.Get("Origin") == s.origin
}
func readBody(w http.ResponseWriter, r *http.Request) (json.RawMessage, error) {
	body, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 16*1024))
	if e != nil || !json.Valid(body) {
		return nil, errors.New("invalid body")
	}
	return body, nil
}
func (s *Server) exchange(w http.ResponseWriter, r *http.Request) {
	if !s.writeAllowed(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	body, e := readBody(w, r)
	var req struct {
		Token string `json:"token"`
	}
	if e != nil || Decode(body, &req) != nil {
		http.Error(w, "invalid bootstrap", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bootstrap == "" || time.Now().After(s.expires) || subtle.ConstantTimeCompare([]byte(req.Token), []byte(s.bootstrap)) != 1 {
		http.Error(w, "bootstrap expired or consumed", http.StatusUnauthorized)
		return
	}
	s.bootstrap = ""
	if e = s.issueSession(w); e != nil {
		http.Error(w, "entropy unavailable", 500)
	}
}
func (s *Server) authorized(next func(http.ResponseWriter, *http.Request, authorization)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, e := r.Cookie("espkvm")
		if e != nil {
			http.Error(w, "unauthorized", 401)
			return
		}
		s.mu.Lock()
		a, ok := s.sessions[cookie.Value]
		if ok && time.Now().After(a.expires) {
			delete(s.sessions, cookie.Value)
			ok = false
		}
		s.mu.Unlock()
		if !ok {
			http.Error(w, "unauthorized", 401)
			return
		}
		// Cookies are not port-scoped. A cookie alone must not reveal the second
		// secret to a different loopback service: reads/SSE need the same CSRF
		// header as writes. The UI retains it in origin-scoped sessionStorage.
		if r.Method == http.MethodGet && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(a.csrf)) != 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next(w, r, a)
	}
}
func (s *Server) events(w http.ResponseWriter, r *http.Request, a authorization) {
	select {
	case s.streams <- struct{}{}:
		defer func() { <-s.streams }()
	default:
		http.Error(w, "too many event streams", 429)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		if time.Now().After(a.expires) {
			return
		}
		body, e := json.Marshal(s.state())
		if e != nil || len(body) > 64*1024 {
			return
		}
		controller := http.NewResponseController(w)
		controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
		id := s.sequence.Add(1)
		if _, e = io.WriteString(w, "id: "+strconv.FormatUint(id, 10)+"\nevent: state\ndata: "+string(body)+"\n\n"); e != nil {
			return
		}
		if controller.Flush() != nil {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
		}
	}
}
func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(value)
}
func Decode(body []byte, value any) error {
	d := json.NewDecoder(strings.NewReader(string(body)))
	d.DisallowUnknownFields()
	if e := d.Decode(value); e != nil {
		return e
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("one JSON value required")
	}
	return nil
}
