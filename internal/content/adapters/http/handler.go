// Package http serves the public content pages and feeds the footer links.
package http

import (
	"errors"
	"log/slog"
	nethttp "net/http"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"

	"github.com/NaheedRayan/goat-architecture/internal/content/app"
	"github.com/NaheedRayan/goat-architecture/internal/content/domain"
	"github.com/NaheedRayan/goat-architecture/internal/platform/httpx"
	"github.com/NaheedRayan/goat-architecture/internal/platform/markdown"
	"github.com/NaheedRayan/goat-architecture/internal/platform/ui"
)

type Handler struct {
	svc *app.Service
	log *slog.Logger
}

func NewHandler(svc *app.Service, log *slog.Logger) *Handler { return &Handler{svc: svc, log: log} }

func (h *Handler) Routes(r chi.Router) { r.Get("/pages/{slug}", h.show) }

// Middleware puts the published page links where the page footer can find them.
func (h *Handler) Middleware(next nethttp.Handler) nethttp.Handler {
	return nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		if r.Method == nethttp.MethodGet || r.Method == nethttp.MethodHead {
			if links, err := h.svc.PublishedLinks(r.Context()); err != nil {
				h.log.Warn("footer links", "err", err)
			} else {
				fl := make([]ui.FooterLink, len(links))
				for i, l := range links {
					fl[i] = ui.FooterLink{Href: "/pages/" + l.Slug, Title: l.Title}
				}
				r = r.WithContext(ui.WithFooterLinks(r.Context(), fl))
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) show(w nethttp.ResponseWriter, r *nethttp.Request) {
	p, err := h.svc.Published(r.Context(), chi.URLParam(r, "slug"))
	if errors.Is(err, domain.ErrNotFound) {
		httpx.Render(w, r, nethttp.StatusNotFound, ui.ErrorPage(404, "We couldn't find that page."))
		return
	}
	if err != nil {
		h.log.Error("content page", "err", err)
		httpx.Render(w, r, nethttp.StatusInternalServerError, ui.ErrorPage(500, "Something went wrong on our side."))
		return
	}
	httpx.Render(w, r, nethttp.StatusOK, Page(p.Title, templ.Raw(markdown.HTML(p.Body)), p.UpdatedAt.Format("2 January 2006")))
}
