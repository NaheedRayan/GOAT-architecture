// Package httpx holds small HTTP helpers shared by module adapters.
package httpx

import (
	"bytes"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5/middleware"
)

// Render buffers the component so a template error cannot produce a half-written page.
func Render(w http.ResponseWriter, r *http.Request, status int, c templ.Component) {
	var buf bytes.Buffer
	if err := c.Render(r.Context(), &buf); err != nil {
		http.Error(w, "render error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

// OriginCheck rejects cross-site state-changing requests. Together with
// SameSite=Lax cookies this is the CSRF defence for form posts. An Origin is
// accepted when its host is the request's Host or one of extraHosts (the public
// hostname, for proxies that rewrite Host).
func OriginCheck(extraHosts ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
			default:
				if o := r.Header.Get("Origin"); o != "" && o != "null" {
					u, err := url.Parse(o)
					if err != nil || (u.Host != r.Host && !slices.Contains(extraHosts, u.Host)) {
						http.Error(w, "cross-origin request blocked", http.StatusForbidden)
						return
					}
				}
				if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
					http.Error(w, "cross-site request blocked", http.StatusForbidden)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// SafeNext returns next only if it is a local path, preventing open redirects.
func SafeNext(next string) string {
	for _, r := range next { // browsers strip tabs/newlines, turning "/\t/evil.com" into "//evil.com"
		if r < 0x20 || r == 0x7f || r == '\\' {
			return "/"
		}
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || u.Opaque != "" ||
		!strings.HasPrefix(u.Path, "/") || strings.HasPrefix(u.Path, "//") {
		return "/"
	}
	// Be strict about the decoded path too, so no encoding trick can reintroduce the above.
	for _, r := range u.Path {
		if r < 0x20 || r == 0x7f || r == '\\' {
			return "/"
		}
	}
	return next
}

// SecurityHeaders sets conservative defaults. hsts should be true only when the
// site is served over HTTPS (production). The CSP covers framing, <base> and
// plugins; it deliberately has no script-src/form-action, because Alpine needs
// eval and payment gateways redirect off-site after a form post.
func SecurityHeaders(hsts bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
			h.Set("Content-Security-Policy", "frame-ancestors 'none'; base-uri 'self'; object-src 'none'")
			h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
			if hsts {
				h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
			}
			next.ServeHTTP(w, r)
		})
	}
}

// EchoRequestID returns the request id to the client so a user can quote it
// when reporting a problem. It must run after chi's RequestID middleware.
func EchoRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id := middleware.GetReqID(r.Context()); id != "" {
			w.Header().Set("X-Request-Id", id)
		}
		next.ServeHTTP(w, r)
	})
}

// ClientIP is the TCP peer's address. Forwarding headers are deliberately not
// consulted here: when the app sits behind a trusted proxy, enable the
// RealIP middleware (TRUST_PROXY=true) which rewrites RemoteAddr first.
func ClientIP(r *http.Request) string {
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	return strings.Trim(host, "[]")
}

// BodyLimitFunc is BodyLimit with a per-request limit (e.g. larger for file uploads).
func BodyLimitFunc(limit func(*http.Request) int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, limit(r))
			}
			next.ServeHTTP(w, r)
		})
	}
}

// BodyLimit caps request bodies so a client cannot make the server buffer huge forms.
func BodyLimit(max int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, max)
			}
			next.ServeHTTP(w, r)
		})
	}
}
