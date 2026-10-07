// Package privacy lets a signed-in person download their data and delete their
// account. It owns no data: export gathers what each module contributes, and
// deletion asks identity to delete the account, which publishes an event that
// every other module reacts to by erasing or anonymising its own records.
package privacy

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/identity"
	"github.com/NaheedRayan/goat-architecture/internal/platform/auth"
	"github.com/NaheedRayan/goat-architecture/internal/platform/httpx"
)

// Exporter returns one module's data about a user, as something JSON-serialisable.
type Exporter func(ctx context.Context, userID uuid.UUID) (any, error)

type Options struct {
	Identity     identity.API
	Exporters    map[string]Exporter // section name -> exporter
	ClearSession func(http.ResponseWriter)
	Log          *slog.Logger
}

type Module struct{ o Options }

func New(o Options) *Module { return &Module{o: o} }

func (m *Module) Routes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(auth.Require)
		r.Get("/account/export", m.export)
		r.Post("/account/delete", m.delete)
	})
	r.Get("/goodbye", func(w http.ResponseWriter, req *http.Request) {
		httpx.Render(w, req, http.StatusOK, GoodbyePage())
	})
}

func (m *Module) export(w http.ResponseWriter, r *http.Request) {
	c, _ := auth.FromContext(r.Context())
	acct, err := m.o.Identity.Export(r.Context(), c.UserID)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	out := map[string]any{"generated_at": time.Now().UTC(), "account": acct}
	for name, ex := range m.o.Exporters {
		data, err := ex(r.Context(), c.UserID)
		if err != nil {
			m.fail(w, r, err)
			return
		}
		out[name] = data
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="my-data.json"`)
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}

func (m *Module) delete(w http.ResponseWriter, r *http.Request) {
	c, _ := auth.FromContext(r.Context())
	err := m.o.Identity.DeleteAccount(r.Context(), c.UserID, r.FormValue("password"))
	switch {
	case errors.Is(err, identity.ErrWrongPassword):
		http.Redirect(w, r, "/account?delete=wrong-password", http.StatusSeeOther)
		return
	case errors.Is(err, identity.ErrLastAdmin):
		http.Redirect(w, r, "/account?delete=last-admin", http.StatusSeeOther)
		return
	case err != nil:
		m.fail(w, r, err)
		return
	}
	m.o.ClearSession(w)
	http.Redirect(w, r, "/goodbye", http.StatusSeeOther)
}

func (m *Module) fail(w http.ResponseWriter, r *http.Request, err error) {
	m.o.Log.Error("privacy", "path", r.URL.Path, "err", err)
	http.Error(w, "something went wrong", http.StatusInternalServerError)
}
