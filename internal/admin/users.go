package admin

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/NaheedRayan/goat-architecture/internal/identity"
	"github.com/NaheedRayan/goat-architecture/internal/platform/httpx"
)

const usersPerPage = 50

func (m *Module) users(w http.ResponseWriter, r *http.Request) {
	m.renderUsers(w, r, "", http.StatusOK)
}

func (m *Module) renderUsers(w http.ResponseWriter, r *http.Request, msg string, status int) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	role := r.URL.Query().Get("role")
	switch role {
	case identity.RoleCustomer, identity.RoleStaff, identity.RoleAdmin:
	default:
		role = ""
	}
	page := min(atoi(r.URL.Query().Get("page"), 1), 10000)
	us, err := m.o.Identity.ListUsers(r.Context(), q, role, usersPerPage+1, (page-1)*usersPerPage)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	hasNext := len(us) > usersPerPage
	if hasNext {
		us = us[:usersPerPage]
	}
	v := url.Values{}
	if q != "" {
		v.Set("q", q)
	}
	if role != "" {
		v.Set("role", role)
	}
	httpx.Render(w, r, status, UsersPage(UsersVM{Users: us, Query: q, Role: role, Page: page, HasNext: hasNext, Self: actor(r).UserID, Message: msg, Params: v.Encode()}))
}

func (m *Module) setUserRole(w http.ResponseWriter, r *http.Request) {
	id, ok := uuidParam(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	role := r.FormValue("role")
	err := m.o.Identity.SetRole(r.Context(), actor(r).UserID, id, role)
	m.userResult(w, r, "user.role", id.String(), map[string]any{"role": role}, err)
}

func (m *Module) setUserStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := uuidParam(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	disable := r.FormValue("action") == "disable"
	err := m.o.Identity.SetDisabled(r.Context(), actor(r).UserID, id, disable)
	action := "user.enable"
	if disable {
		action = "user.disable"
	}
	m.userResult(w, r, action, id.String(), nil, err)
}

func (m *Module) userResult(w http.ResponseWriter, r *http.Request, action, id string, detail map[string]any, err error) {
	var ve identity.ValidationError
	switch {
	case err == nil:
		m.audit(r, action, "user", id, detail)
		http.Redirect(w, r, "/admin/users?"+r.URL.RawQuery, http.StatusSeeOther)
	case errors.Is(err, identity.ErrNotFound):
		http.NotFound(w, r)
	case errors.Is(err, identity.ErrSelfChange), errors.Is(err, identity.ErrLastAdmin):
		m.renderUsers(w, r, err.Error(), http.StatusConflict)
	case errors.As(err, &ve):
		m.renderUsers(w, r, ve.Error(), http.StatusUnprocessableEntity)
	default:
		m.fail(w, r, err)
	}
}
