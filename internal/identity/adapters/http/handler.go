// Package http serves the identity pages and the authentication middleware.
package http

import (
	"errors"
	"log/slog"
	nethttp "net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/identity/app"
	"github.com/NaheedRayan/goat-architecture/internal/identity/domain"
	"github.com/NaheedRayan/goat-architecture/internal/platform/auth"
	"github.com/NaheedRayan/goat-architecture/internal/platform/httpx"
	"github.com/NaheedRayan/goat-architecture/internal/platform/ratelimit"
	"github.com/NaheedRayan/goat-architecture/internal/platform/ui"
)

const (
	accessCookie  = "access_token"
	refreshCookie = "refresh_token"
)

type Handler struct {
	svc          *app.Service
	signer       *auth.Signer
	secure       bool
	accessMaxAge time.Duration
	log          *slog.Logger

	// Password guessing and argon2 CPU abuse are the main threats on these routes.
	loginIP, loginAccount, registerIP *ratelimit.Limiter
}

func NewHandler(svc *app.Service, signer *auth.Signer, secureCookies bool, accessTTL time.Duration, log *slog.Logger) *Handler {
	return &Handler{
		svc: svc, signer: signer, secure: secureCookies, accessMaxAge: accessTTL, log: log,
		loginIP:      ratelimit.New(30, 2*time.Second), // 30 burst, then 30/min per client
		loginAccount: ratelimit.New(6, 30*time.Second), // 6 burst, then 2/min per client+account
		registerIP:   ratelimit.New(10, 6*time.Minute), // 10 burst, then 10/hour per client
	}
}

func (h *Handler) Routes(r chi.Router) {
	r.Get("/register", h.registerForm)
	r.Post("/register", h.register)
	r.Get("/login", h.loginForm)
	r.Post("/login", h.login)
	r.Post("/logout", h.logout)
	r.Group(func(r chi.Router) {
		r.Use(auth.Require)
		r.Get("/account", h.account)
		r.Post("/account/addresses", h.addAddress)
		r.Post("/account/addresses/{id}/delete", h.deleteAddress)
	})
}

// Authenticate loads the caller's identity into the request context. It accepts
// a Bearer token (service-to-service / API clients) or the access cookie, and
// transparently rotates the refresh cookie when the access token has expired.
func (h *Handler) Authenticate(next nethttp.Handler) nethttp.Handler {
	return nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
			if c, err := h.signer.Verify(tok); err == nil {
				r = r.WithContext(auth.WithClaims(r.Context(), c))
			}
			next.ServeHTTP(w, r)
			return
		}
		if ck, err := r.Cookie(accessCookie); err == nil {
			if c, err := h.signer.Verify(ck.Value); err == nil {
				next.ServeHTTP(w, r.WithContext(auth.WithClaims(r.Context(), c)))
				return
			}
		}
		if rk, err := r.Cookie(refreshCookie); err == nil && r.URL.Path != "/logout" {
			sess, err := h.svc.Refresh(r.Context(), rk.Value)
			if err != nil {
				h.clearCookies(w)
			} else if c, err := h.signer.Verify(sess.AccessToken); err == nil {
				h.setCookies(w, sess)
				r = r.WithContext(auth.WithClaims(r.Context(), c))
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) setCookies(w nethttp.ResponseWriter, s app.Session) {
	nethttp.SetCookie(w, &nethttp.Cookie{
		Name: accessCookie, Value: s.AccessToken, Path: "/", HttpOnly: true, Secure: h.secure,
		SameSite: nethttp.SameSiteLaxMode, MaxAge: int(h.accessMaxAge.Seconds()),
	})
	if s.RefreshToken == "" { // access-token-only grant after a refresh race
		return
	}
	nethttp.SetCookie(w, &nethttp.Cookie{
		Name: refreshCookie, Value: s.RefreshToken, Path: "/", HttpOnly: true, Secure: h.secure,
		SameSite: nethttp.SameSiteLaxMode, Expires: s.RefreshExpiresAt,
	})
}

// throttled answers 429 when any limiter refuses the key.
func (h *Handler) throttled(w nethttp.ResponseWriter, r *nethttp.Request, checks ...func() (bool, time.Duration)) bool {
	for _, check := range checks {
		if ok, wait := check(); !ok {
			secs := int(wait.Seconds()) + 1
			w.Header().Set("Retry-After", strconv.Itoa(secs))
			httpx.Render(w, r, nethttp.StatusTooManyRequests, ui.ErrorPage(429, "Too many attempts. Please wait a bit and try again."))
			return true
		}
	}
	return false
}

func (h *Handler) clearCookies(w nethttp.ResponseWriter) {
	for _, n := range []string{accessCookie, refreshCookie} {
		nethttp.SetCookie(w, &nethttp.Cookie{Name: n, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: h.secure, SameSite: nethttp.SameSiteLaxMode})
	}
}

func (h *Handler) registerForm(w nethttp.ResponseWriter, r *nethttp.Request) {
	httpx.Render(w, r, nethttp.StatusOK, RegisterPage(RegisterForm{}, ""))
}

func (h *Handler) register(w nethttp.ResponseWriter, r *nethttp.Request) {
	if h.throttled(w, r, func() (bool, time.Duration) { return h.registerIP.Allow(httpx.ClientIP(r)) }) {
		return
	}
	form := RegisterForm{Name: r.FormValue("name"), Email: r.FormValue("email")}
	sess, err := h.svc.Register(r.Context(), form.Email, form.Name, r.FormValue("password"))
	if err != nil {
		var ve domain.ValidationError
		if errors.As(err, &ve) || errors.Is(err, domain.ErrEmailTaken) {
			httpx.Render(w, r, nethttp.StatusUnprocessableEntity, RegisterPage(form, err.Error()))
			return
		}
		h.fail(w, r, err)
		return
	}
	h.setCookies(w, sess)
	nethttp.Redirect(w, r, "/", nethttp.StatusSeeOther)
}

func (h *Handler) loginForm(w nethttp.ResponseWriter, r *nethttp.Request) {
	httpx.Render(w, r, nethttp.StatusOK, LoginPage("", httpx.SafeNext(r.URL.Query().Get("next")), ""))
}

func (h *Handler) login(w nethttp.ResponseWriter, r *nethttp.Request) {
	email, next := r.FormValue("email"), httpx.SafeNext(r.FormValue("next"))
	ip := httpx.ClientIP(r)
	if h.throttled(w, r,
		func() (bool, time.Duration) { return h.loginIP.Allow(ip) },
		func() (bool, time.Duration) {
			return h.loginAccount.Allow(ip + "|" + strings.ToLower(strings.TrimSpace(email)))
		},
	) {
		return
	}
	sess, err := h.svc.Login(r.Context(), email, r.FormValue("password"))
	if err != nil {
		if errors.Is(err, domain.ErrInvalidCredentials) {
			httpx.Render(w, r, nethttp.StatusUnauthorized, LoginPage(email, next, err.Error()))
			return
		}
		h.fail(w, r, err)
		return
	}
	h.setCookies(w, sess)
	nethttp.Redirect(w, r, next, nethttp.StatusSeeOther)
}

func (h *Handler) logout(w nethttp.ResponseWriter, r *nethttp.Request) {
	if rk, err := r.Cookie(refreshCookie); err == nil {
		_ = h.svc.Logout(r.Context(), rk.Value)
	}
	h.clearCookies(w)
	nethttp.Redirect(w, r, "/", nethttp.StatusSeeOther)
}

func (h *Handler) account(w nethttp.ResponseWriter, r *nethttp.Request) {
	h.renderAccount(w, r, AddressForm{}, "", nethttp.StatusOK)
}

func (h *Handler) renderAccount(w nethttp.ResponseWriter, r *nethttp.Request, form AddressForm, msg string, status int) {
	c, _ := auth.FromContext(r.Context())
	addrs, err := h.svc.ListAddresses(r.Context(), c.UserID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.Render(w, r, status, AccountPage(addrs, form, msg))
}

func (h *Handler) addAddress(w nethttp.ResponseWriter, r *nethttp.Request) {
	c, _ := auth.FromContext(r.Context())
	form := AddressForm{
		FullName: r.FormValue("full_name"), Phone: r.FormValue("phone"), Line1: r.FormValue("line1"),
		Line2: r.FormValue("line2"), City: r.FormValue("city"), PostalCode: r.FormValue("postal_code"),
		Country: r.FormValue("country"), IsDefault: r.FormValue("is_default") != "",
	}
	err := h.svc.AddAddress(r.Context(), domain.Address{
		UserID: c.UserID, FullName: form.FullName, Phone: form.Phone, Line1: form.Line1, Line2: form.Line2,
		City: form.City, PostalCode: form.PostalCode, Country: form.Country, IsDefault: form.IsDefault,
	})
	var ve domain.ValidationError
	if errors.As(err, &ve) {
		h.renderAccount(w, r, form, ve.Error(), nethttp.StatusUnprocessableEntity)
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	nethttp.Redirect(w, r, "/account", nethttp.StatusSeeOther)
}

func (h *Handler) deleteAddress(w nethttp.ResponseWriter, r *nethttp.Request) {
	c, _ := auth.FromContext(r.Context())
	aid, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		nethttp.NotFound(w, r)
		return
	}
	if err := h.svc.DeleteAddress(r.Context(), c.UserID, aid); err != nil {
		h.fail(w, r, err)
		return
	}
	nethttp.Redirect(w, r, "/account", nethttp.StatusSeeOther)
}

func (h *Handler) fail(w nethttp.ResponseWriter, r *nethttp.Request, err error) {
	h.log.Error("identity http", "path", r.URL.Path, "err", err)
	nethttp.Error(w, "something went wrong", nethttp.StatusInternalServerError)
}
