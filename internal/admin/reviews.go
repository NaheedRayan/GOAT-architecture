package admin

import (
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/platform/httpx"
	"github.com/NaheedRayan/goat-architecture/internal/review"
)

const reviewsPerPage = 30

// ReviewRow is a review with its product's name and slug resolved.
type ReviewRow struct {
	Review      review.Review
	ProductName string
	ProductSlug string
}

func (m *Module) reviews(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status != review.StatusPublished && status != review.StatusHidden {
		status = ""
	}
	page := min(atoi(r.URL.Query().Get("page"), 1), 10000)
	rs, err := m.o.Reviews.All(r.Context(), status, reviewsPerPage+1, (page-1)*reviewsPerPage)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	hasNext := len(rs) > reviewsPerPage
	if hasNext {
		rs = rs[:reviewsPerPage]
	}
	ids := make([]uuid.UUID, len(rs))
	for i, x := range rs {
		ids[i] = x.ProductID
	}
	rows := make([]ReviewRow, len(rs))
	for i, x := range rs {
		rows[i] = ReviewRow{Review: x, ProductName: "(removed product)"}
	}
	if len(ids) > 0 {
		ps, err := m.o.Catalog.ByIDs(r.Context(), ids)
		if err != nil {
			m.fail(w, r, err)
			return
		}
		byID := map[uuid.UUID]int{}
		for i, p := range ps {
			byID[p.ID] = i
		}
		for i := range rows {
			if j, ok := byID[rows[i].Review.ProductID]; ok {
				rows[i].ProductName, rows[i].ProductSlug = ps[j].Name, ps[j].Slug
			}
		}
	}
	httpx.Render(w, r, http.StatusOK, ReviewsPage(rows, status, page, hasNext))
}

func (m *Module) reviewStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := uuidParam(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	status := r.FormValue("status")
	err := m.o.Reviews.SetStatus(r.Context(), id, status)
	if errors.Is(err, review.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	var ve review.ValidationError
	if errors.As(err, &ve) {
		http.Error(w, ve.Error(), http.StatusBadRequest)
		return
	}
	if err != nil {
		m.fail(w, r, err)
		return
	}
	m.audit(r, "review."+status, "review", id.String(), nil)
	http.Redirect(w, r, "/admin/reviews", http.StatusSeeOther)
}

func (m *Module) reviewDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := uuidParam(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := m.o.Reviews.Delete(r.Context(), id); err != nil && !errors.Is(err, review.ErrNotFound) {
		m.fail(w, r, err)
		return
	}
	m.audit(r, "review.delete", "review", id.String(), nil)
	http.Redirect(w, r, "/admin/reviews", http.StatusSeeOther)
}
