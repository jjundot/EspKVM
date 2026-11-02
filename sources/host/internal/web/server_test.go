package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

const origin = "http://127.0.0.1:62626"

func request(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, origin+path, strings.NewReader(body))
	r.Header.Set("Origin", origin)
	r.Header.Set("Content-Type", "application/json")
	return r
}
func testServer(t *testing.T) (*Server, string, *int) {
	t.Helper()
	calls := new(int)
	s, token, e := New("127.0.0.1:62626", func() State { return State{} }, func(context.Context, string, json.RawMessage) (any, error) {
		*calls++
		return map[string]bool{"ok": true}, nil
	}, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<h1>EspKVM</h1>")}})
	if e != nil {
		t.Fatal(e)
	}
	return s, token, calls
}
func login(t *testing.T, s *Server, token string) (*http.Cookie, string) {
	t.Helper()
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, request("POST", "/api/bootstrap", `{"token":"`+token+`"}`))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal(cookies)
	}
	var body struct {
		CSRF string `json:"csrf"`
	}
	json.Unmarshal(w.Body.Bytes(), &body)
	return cookies[0], body.CSRF
}
func TestBootstrapIsOneTimeAndNotInPage(t *testing.T) {
	s, token, _ := testServer(t)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, request("GET", "/", ""))
	if strings.Contains(w.Body.String(), token) {
		t.Fatal("bootstrap exposed by initial page")
	}
	cookie, csrf := login(t, s, token)
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || csrf == "" {
		t.Fatal("unsafe session cookie", cookie)
	}
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, request("POST", "/api/bootstrap", `{"token":"`+token+`"}`))
	if w.Code != 401 {
		t.Fatal("bootstrap replay accepted", w.Code)
	}
}
func TestHostOriginCookieAndCSRF(t *testing.T) {
	s, token, calls := testServer(t)
	cookie, csrf := login(t, s, token)
	for _, tc := range []struct {
		name   string
		mutate func(*http.Request)
	}{
		{"host", func(r *http.Request) { r.Host = "evil.example" }},
		{"origin", func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }},
		{"null origin", func(r *http.Request) { r.Header.Set("Origin", "null") }},
		{"missing origin", func(r *http.Request) { r.Header.Del("Origin") }},
		{"csrf", func(r *http.Request) { r.Header.Del("X-CSRF-Token") }},
		{"cookie", func(r *http.Request) { r.Header.Del("Cookie") }},
		{"form", func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := request("POST", "/api/target", `{}`)
			r.AddCookie(cookie)
			r.Header.Set("X-CSRF-Token", csrf)
			tc.mutate(r)
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			if w.Code < 400 {
				t.Fatal("unauthorized write", w.Code)
			}
		})
	}
	if *calls != 0 {
		t.Fatal("invalid request had side effects")
	}
	r := request("POST", "/api/target", `{}`)
	r.AddCookie(cookie)
	r.Header.Set("X-CSRF-Token", csrf)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 || *calls != 1 {
		t.Fatal(w.Code, *calls)
	}
	for _, path := range []string{"/api/state", "/api/session", "/api/events"} {
		w = httptest.NewRecorder()
		s.Handler().ServeHTTP(w, request("GET", path, ""))
		if w.Code != 401 {
			t.Fatal("unauthenticated sensitive read", path, w.Code)
		}
		w = httptest.NewRecorder()
		r := request("GET", path, "")
		r.AddCookie(cookie)
		s.Handler().ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal("cookie-only secret discovery accepted", path, w.Code)
		}
	}
}
func TestBootstrapExpiryAndBodyLimit(t *testing.T) {
	s, token, calls := testServer(t)
	s.expires = time.Now().Add(-time.Second)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, request("POST", "/api/bootstrap", `{"token":"`+token+`"}`))
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	s, token, calls = testServer(t)
	cookie, csrf := login(t, s, token)
	r := request("POST", "/api/target", `{"x":"`+strings.Repeat("a", 17*1024)+`"}`)
	r.AddCookie(cookie)
	r.Header.Set("X-CSRF-Token", csrf)
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code < 400 || *calls != 0 {
		t.Fatal("oversized body executed")
	}
}

type cancelWriter struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
}

func (w cancelWriter) Flush() { w.ResponseRecorder.Flush(); w.cancel() }
func TestSSEIsAuthenticatedAndBounded(t *testing.T) {
	s, token, _ := testServer(t)
	cookie, csrf := login(t, s, token)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := request("GET", "/api/events", "").WithContext(ctx)
	r.AddCookie(cookie)
	r.Header.Set("X-CSRF-Token", csrf)
	w := cancelWriter{httptest.NewRecorder(), cancel}
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "event: state\n") || len(s.streams) != 0 {
		t.Fatal("SSE lifecycle failed", w.Body.String())
	}
}
