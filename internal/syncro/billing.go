package syncro

import (
	"context"
	"strconv"
	"strings"
)

// Invoice is the trimmed view of a Syncro invoice.
type Invoice struct {
	ID         int64   `json:"id"`
	Number     string  `json:"number,omitempty"`
	CustomerID int64   `json:"customer_id,omitempty"`
	Customer   string  `json:"customer,omitempty"`
	Total      float64 `json:"total"`
	Balance    float64 `json:"balance_due"`
	Paid       bool    `json:"paid"`
	DueDate    string  `json:"due_date,omitempty"`
	CreatedAt  string  `json:"created_at,omitempty"`
	TicketID   int64   `json:"ticket_id,omitempty"`
}

// InvoiceSearch filters invoices. Syncro exposes paid and unpaid as separate
// flags rather than a status, and since_updated_at is what an incremental
// refresh will hang off.
type InvoiceSearch struct {
	// Status is "", "paid" or "unpaid".
	Status         string
	CustomerID     int64
	TicketID       int64
	SinceUpdatedAt string
	Page           int
	PerPage        int
}

// ListInvoices returns a page of invoices.
func (c *Client) ListInvoices(ctx context.Context, opts InvoiceSearch) (Result[Invoice], error) {
	q := paging(opts.Page, opts.PerPage)
	switch strings.ToLower(opts.Status) {
	case "paid":
		q.Set("paid", "true")
	case "unpaid":
		q.Set("unpaid", "true")
	}
	if opts.TicketID > 0 {
		q.Set("ticket_id", strconv.FormatInt(opts.TicketID, 10))
	}
	if opts.SinceUpdatedAt != "" {
		q.Set("since_updated_at", opts.SinceUpdatedAt)
	}

	var body struct {
		Invoices []wireInvoice `json:"invoices"`
		Meta     wireMeta      `json:"meta"`
	}
	if err := c.get(ctx, "/invoices", q, &body); err != nil {
		return Result[Invoice]{}, err
	}

	out := Result[Invoice]{Items: make([]Invoice, 0, len(body.Invoices))}
	for _, w := range body.Invoices {
		inv := w.trim()
		// Syncro has no customer_id filter on this endpoint, so the selection
		// happens here rather than in the query.
		if opts.CustomerID > 0 && inv.CustomerID != opts.CustomerID {
			continue
		}
		out.Items = append(out.Items, inv)
	}
	out.Page = pageOf(body.Meta, opts.Page, opts.PerPage)
	return out, nil
}

// Standing is a customer's financial position.
//
// Syncro exposes no balance field on a customer — verified against a live
// account with the "View Total Invoiced" permission granted — so the balance
// is summed from unpaid invoices here. Saying where a number came from matters
// more than the number when someone is about to quote it to a customer.
type Standing struct {
	CustomerID     int64     `json:"customer_id"`
	Customer       string    `json:"customer,omitempty"`
	OutstandingSum float64   `json:"outstanding_total"`
	OutstandingQty int       `json:"outstanding_count"`
	OverdueQty     int       `json:"overdue_count"`
	OldestDueDate  string    `json:"oldest_due_date,omitempty"`
	Unpaid         []Invoice `json:"unpaid_invoices"`
	RecentlyPaid   []Invoice `json:"recently_paid,omitempty"`

	// Derived explains how the totals were reached, since a figure a
	// technician might repeat to a customer should never look authoritative
	// when it is inferred.
	Derived string `json:"derived"`
	// Partial is set when paging cut the result short, so a total is not
	// mistaken for complete.
	Partial bool `json:"partial,omitempty"`
}

// CustomerStanding summarises what a customer owes.
func (c *Client) CustomerStanding(ctx context.Context, customerID int64, includePaid bool) (Standing, error) {
	s := Standing{
		CustomerID: customerID,
		Unpaid:     []Invoice{},
		Derived:    "summed from unpaid invoices; Syncro exposes no customer balance field",
	}

	unpaid, err := c.ListInvoices(ctx, InvoiceSearch{
		Status: "unpaid", CustomerID: customerID, PerPage: maxPerPage,
	})
	if err != nil {
		return Standing{}, err
	}
	s.Partial = unpaid.Page.TotalPages > 1

	today := nowRFC3339()[:10]
	for _, inv := range unpaid.Items {
		s.Unpaid = append(s.Unpaid, inv)
		s.OutstandingSum += inv.Balance
		s.OutstandingQty++
		if inv.Customer != "" && s.Customer == "" {
			s.Customer = inv.Customer
		}
		if inv.DueDate != "" {
			if inv.DueDate[:min(10, len(inv.DueDate))] < today {
				s.OverdueQty++
			}
			if s.OldestDueDate == "" || inv.DueDate < s.OldestDueDate {
				s.OldestDueDate = inv.DueDate
			}
		}
	}

	if includePaid {
		paid, err := c.ListInvoices(ctx, InvoiceSearch{
			Status: "paid", CustomerID: customerID, PerPage: 25,
		})
		if err == nil {
			s.RecentlyPaid = paid.Items
		}
	}

	return s, nil
}

type wireInvoice struct {
	ID         int64  `json:"id"`
	Number     any    `json:"number"`
	CustomerID int64  `json:"customer_id"`
	TicketID   int64  `json:"ticket_id"`
	DueDate    string `json:"due_date"`
	CreatedAt  string `json:"created_at"`

	// Amounts arrive as either strings or numbers depending on endpoint, and
	// under more than one name. Decoding several beats a silently zero total
	// on a figure someone might quote to a customer.
	Total        any    `json:"total"`
	GrandTotal   any    `json:"grand_total"`
	BalanceDue   any    `json:"balance_due"`
	Balance      any    `json:"balance"`
	IsPaid       *bool  `json:"is_paid"`
	PaidStatus   any    `json:"paid"`
	CustomerName string `json:"customer_business_then_name"`
	Customer     *struct {
		BusinessName string `json:"business_name"`
		FullName     string `json:"fullname"`
	} `json:"customer"`
}

func (w wireInvoice) trim() Invoice {
	inv := Invoice{
		ID:         w.ID,
		Number:     stringify(w.Number),
		CustomerID: w.CustomerID,
		TicketID:   w.TicketID,
		DueDate:    w.DueDate,
		CreatedAt:  w.CreatedAt,
		Total:      firstNumber(w.Total, w.GrandTotal),
		Balance:    firstNumber(w.BalanceDue, w.Balance),
	}

	inv.Customer = w.CustomerName
	if inv.Customer == "" && w.Customer != nil {
		inv.Customer = firstNonEmpty(w.Customer.BusinessName, w.Customer.FullName)
	}

	switch {
	case w.IsPaid != nil:
		inv.Paid = *w.IsPaid
	case w.PaidStatus != nil:
		inv.Paid = truthy(w.PaidStatus)
	default:
		// No explicit flag: a zero balance is the only signal left.
		inv.Paid = inv.Balance == 0 && inv.Total > 0
	}
	return inv
}

// firstNumber returns the first value that parses as a number, tolerating the
// string-or-number inconsistency vendors are prone to on money fields.
func firstNumber(values ...any) float64 {
	for _, v := range values {
		switch t := v.(type) {
		case float64:
			if t != 0 {
				return t
			}
		case string:
			cleaned := strings.TrimSpace(strings.NewReplacer("$", "", ",", "").Replace(t))
			if f, err := strconv.ParseFloat(cleaned, 64); err == nil && f != 0 {
				return f
			}
		}
	}
	return 0
}

func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return strings.EqualFold(t, "true") || strings.EqualFold(t, "paid")
	case float64:
		return t != 0
	}
	return false
}
