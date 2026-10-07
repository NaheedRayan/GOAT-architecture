package admin

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/identity"
	"github.com/NaheedRayan/goat-architecture/internal/order"
)

type LowStockRow struct {
	ProductID uuid.UUID
	Name      string
	Label     string
	SKU       string
	Available int
}

type DashboardVM struct {
	DraftPages     int
	Currency       string
	Days           int
	Report         order.Report
	Series         []order.DayStat
	Counts         []StatusCount
	PendingReturns int
	LowStock       []LowStockRow
	Threshold      int
	IsAdmin        bool
}

type OrdersVM struct {
	Orders         []order.Order
	Filter         OrderFilter
	Page           int
	HasNext        bool
	PendingReturns int
}

type OrderVM struct {
	Order          order.Order
	IsAdmin        bool
	Done           string // success banner
	Error          string
	ReturnDeadline time.Time
}

type UsersVM struct {
	Users   []identity.User
	Query   string
	Role    string
	Page    int
	HasNext bool
	Self    uuid.UUID
	Message string
	Params  string // the current filter as a query string, for pagination and form actions
}

type AuditVM struct {
	Rows    []AuditRow
	Action  string
	Entity  string
	Page    int
	HasNext bool
	Params  string
}

// bar is one column of the sales chart, in SVG user units.
type bar struct {
	X, Y, W, H float64
	Label      string
}

// chartBars lays out one bar per day on a 100-high canvas (the tallest day fills it).
func chartBars(series []order.DayStat, currencyFmt func(int64) string) (bars []bar, width float64) {
	var peak int64
	for _, d := range series {
		peak = max(peak, d.RevenueCents)
	}
	const slot, gap = 12.0, 3.0
	for i, d := range series {
		h := 0.0
		if peak > 0 {
			h = float64(d.RevenueCents) / float64(peak) * 96
		}
		if d.RevenueCents > 0 && h < 2 {
			h = 2 // a quiet but non-zero day stays visible
		}
		bars = append(bars, bar{
			X: float64(i) * slot, Y: 100 - h, W: slot - gap, H: h,
			Label: fmt.Sprintf("%s: %s, %d orders", d.Day.Format("2 Jan"), currencyFmt(d.RevenueCents), d.Orders),
		})
	}
	return bars, float64(len(series)) * slot
}
