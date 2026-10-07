package admin

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/platform/audit"
	"github.com/NaheedRayan/goat-architecture/internal/platform/httpx"
)

const auditPerPage = 50

// AuditRow is one audit entry with the actor's email resolved.
type AuditRow struct {
	Entry audit.Entry
	Actor string
}

func (m *Module) auditLog(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := audit.Filter{Action: strings.TrimSpace(q.Get("action")), Entity: strings.TrimSpace(q.Get("entity"))}
	page := min(atoi(q.Get("page"), 1), 10000)
	entries, err := m.o.Audit.List(r.Context(), f, auditPerPage+1, (page-1)*auditPerPage)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	hasNext := len(entries) > auditPerPage
	if hasNext {
		entries = entries[:auditPerPage]
	}
	names := map[uuid.UUID]string{}
	rows := make([]AuditRow, len(entries))
	for i, e := range entries {
		rows[i] = AuditRow{Entry: e, Actor: "system"}
		if e.ActorID == nil {
			continue
		}
		name, seen := names[*e.ActorID]
		if !seen {
			name = "(deleted user)"
			if u, err := m.o.Identity.User(r.Context(), *e.ActorID); err == nil {
				name = u.Email
			}
			names[*e.ActorID] = name
		}
		rows[i].Actor = name
	}
	v := url.Values{}
	if f.Action != "" {
		v.Set("action", f.Action)
	}
	if f.Entity != "" {
		v.Set("entity", f.Entity)
	}
	httpx.Render(w, r, http.StatusOK, AuditPage(AuditVM{Rows: rows, Action: f.Action, Entity: f.Entity, Page: page, HasNext: hasNext, Params: v.Encode()}))
}
