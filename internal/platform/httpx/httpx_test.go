package httpx

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientIPIgnoresForwardingHeaders(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "203.0.113.9:51234"
	r.Header.Set("X-Forwarded-For", "1.2.3.4")
	r.Header.Set("X-Real-IP", "5.6.7.8")
	if got := ClientIP(r); got != "203.0.113.9" {
		t.Fatalf("ClientIP = %q, want the TCP peer", got)
	}
	r.RemoteAddr = "[2001:db8::1]:443"
	if got := ClientIP(r); got != "2001:db8::1" {
		t.Fatalf("ClientIP(v6) = %q", got)
	}
}

func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{
		"/orders": "/orders", "/a?b=c": "/a?b=c", "//evil.com": "/", "/\t/evil.com": "/", "/\n/evil.com": "/", "/%09/evil.com": "/", "http:evil.com": "/", "https://evil.com": "/", "": "/", `/\evil.com`: "/", "javascript:x": "/",
	} {
		if got := SafeNext(in); got != want {
			t.Errorf("SafeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBodyLimit(t *testing.T) {
	var readErr error
	h := BodyLimit(10)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/", strings.NewReader(strings.Repeat("x", 100))))
	if readErr == nil {
		t.Fatal("oversized body was not rejected")
	}
	readErr = nil
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/", strings.NewReader("small")))
	if readErr != nil {
		t.Fatalf("small body rejected: %v", readErr)
	}
}

func TestSecurityHeadersHSTSOnlyWhenAsked(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {})
	for _, hsts := range []bool{false, true} {
		w := httptest.NewRecorder()
		SecurityHeaders(hsts)(ok).ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
		if got := w.Header().Get("Strict-Transport-Security") != ""; got != hsts {
			t.Errorf("hsts=%v but header present=%v", hsts, got)
		}
		if w.Header().Get("Content-Security-Policy") == "" {
			t.Error("CSP missing")
		}
	}
}
