package http

import (
	"errors"
	nethttp "net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/identity/app"
	"github.com/NaheedRayan/goat-architecture/internal/identity/domain"
	"github.com/NaheedRayan/goat-architecture/internal/platform/auth"
	"github.com/NaheedRayan/goat-architecture/internal/platform/httpx"
)

// AccountView is everything the account page shows.
type AccountView struct {
	User         domain.User
	Addresses    []domain.Address
	AddressForm  AddressForm
	AddressError string
	ProfileMsg   string // error text for the profile form
	PasswordMsg  string
	Notice       string // one-off banner, e.g. "verification email sent"
	DeleteError  string
}

// ---- forgot / reset ----

func (h *Handler) forgotForm(w nethttp.ResponseWriter, r *nethttp.Request) {
	httpx.Render(w, r, nethttp.StatusOK, ForgotPage("", false))
}

func (h *Handler) forgot(w nethttp.ResponseWriter, r *nethttp.Request) {
	email := strings.TrimSpace(r.FormValue("email"))
	ip := httpx.ClientIP(r)
	if h.throttled(w, r,
		func() (bool, time.Duration) { return h.forgotIP.Allow(ip) },
		func() (bool, time.Duration) { return h.forgotAccount.Allow(ip + "|" + strings.ToLower(email)) },
	) {
		return
	}
	if err := h.svc.RequestPasswordReset(r.Context(), email); err != nil {
		h.fail(w, r, err)
		return
	}
	// Always the same answer, whether or not the address has an account.
	httpx.Render(w, r, nethttp.StatusOK, ForgotPage(email, true))
}

func (h *Handler) resetForm(w nethttp.ResponseWriter, r *nethttp.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		httpx.Render(w, r, nethttp.StatusBadRequest, ResetPage("", "That reset link is incomplete. Request a new one."))
		return
	}
	httpx.Render(w, r, nethttp.StatusOK, ResetPage(token, ""))
}

func (h *Handler) reset(w nethttp.ResponseWriter, r *nethttp.Request) {
	token := r.FormValue("token")
	if r.FormValue("password") != r.FormValue("confirm") {
		httpx.Render(w, r, nethttp.StatusUnprocessableEntity, ResetPage(token, "The two passwords do not match."))
		return
	}
	sess, err := h.svc.ResetPassword(r.Context(), token, r.FormValue("password"))
	var ve domain.ValidationError
	switch {
	case errors.As(err, &ve):
		httpx.Render(w, r, nethttp.StatusUnprocessableEntity, ResetPage(token, ve.Error()))
		return
	case errors.Is(err, domain.ErrInvalidToken):
		httpx.Render(w, r, nethttp.StatusBadRequest, ResetPage("", "That reset link is invalid or has expired. Request a new one."))
		return
	case err != nil:
		h.fail(w, r, err)
		return
	}
	h.setCookies(w, sess)
	nethttp.Redirect(w, r, "/account?notice=password-set", nethttp.StatusSeeOther)
}

// ---- email verification ----

func (h *Handler) verify(w nethttp.ResponseWriter, r *nethttp.Request) {
	err := h.svc.VerifyEmail(r.Context(), r.URL.Query().Get("token"))
	switch {
	case err == nil:
		httpx.Render(w, r, nethttp.StatusOK, VerifyPage(true))
	case errors.Is(err, domain.ErrInvalidToken):
		httpx.Render(w, r, nethttp.StatusBadRequest, VerifyPage(false))
	default:
		h.fail(w, r, err)
	}
}

func (h *Handler) resendVerification(w nethttp.ResponseWriter, r *nethttp.Request) {
	c, _ := auth.FromContext(r.Context())
	if h.throttled(w, r, func() (bool, time.Duration) { return h.resend.Allow(c.UserID.String()) }) {
		return
	}
	if err := h.svc.ResendVerification(r.Context(), c.UserID); err != nil {
		h.fail(w, r, err)
		return
	}
	nethttp.Redirect(w, r, "/account?notice=verification-sent", nethttp.StatusSeeOther)
}

// ---- profile and password ----

func (h *Handler) updateProfile(w nethttp.ResponseWriter, r *nethttp.Request) {
	c, _ := auth.FromContext(r.Context())
	err := h.svc.UpdateProfile(r.Context(), c.UserID, r.FormValue("name"))
	if err == nil {
		err = h.svc.SetCartReminders(r.Context(), c.UserID, r.FormValue("cart_reminders") != "")
	}
	var ve domain.ValidationError
	if errors.As(err, &ve) {
		h.renderAccountWith(w, r, AccountView{ProfileMsg: ve.Error()}, nethttp.StatusUnprocessableEntity)
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	nethttp.Redirect(w, r, "/account?notice=profile-saved", nethttp.StatusSeeOther)
}

func (h *Handler) changePassword(w nethttp.ResponseWriter, r *nethttp.Request) {
	c, _ := auth.FromContext(r.Context())
	if r.FormValue("new_password") != r.FormValue("confirm") {
		h.renderAccountWith(w, r, AccountView{PasswordMsg: "The two new passwords do not match."}, nethttp.StatusUnprocessableEntity)
		return
	}
	sess, err := h.svc.ChangePassword(r.Context(), c.UserID, r.FormValue("current_password"), r.FormValue("new_password"))
	var ve domain.ValidationError
	switch {
	case errors.As(err, &ve):
		h.renderAccountWith(w, r, AccountView{PasswordMsg: ve.Error()}, nethttp.StatusUnprocessableEntity)
		return
	case errors.Is(err, domain.ErrWrongPassword):
		h.renderAccountWith(w, r, AccountView{PasswordMsg: err.Error()}, nethttp.StatusUnprocessableEntity)
		return
	case err != nil:
		h.fail(w, r, err)
		return
	}
	h.setCookies(w, sess) // every other session was signed out; this one continues
	nethttp.Redirect(w, r, "/account?notice=password-changed", nethttp.StatusSeeOther)
}

// ---- helpers for other modules ----

// GuestSignIn creates a guest account and signs the browser in.
func (h *Handler) GuestSignIn(w nethttp.ResponseWriter, r *nethttp.Request, email, name string) (uuid.UUID, error) {
	if ok, _ := h.registerIP.Allow(httpx.ClientIP(r)); !ok { // guest accounts are accounts: share the registration budget
		return uuid.Nil, app.ErrTooManyAttempts
	}
	sess, err := h.svc.GuestCheckout(r.Context(), email, name)
	if err != nil {
		return uuid.Nil, err
	}
	h.setCookies(w, sess)
	return sess.User.ID, nil
}

// ClearSession removes the session cookies.
func (h *Handler) ClearSession(w nethttp.ResponseWriter) { h.clearCookies(w) }
