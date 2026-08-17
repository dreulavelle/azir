package syncro

import (
	"context"
	"strconv"
	"strings"
)

// Contacts lists the people at a customer.
//
// A customer record carries one address and one number — the company's. When a
// ticket stalls and somebody has to be called, the useful question is who at
// that company to call, and that is a different endpoint.
func (c *Client) Contacts(ctx context.Context, customerID int64, page, perPage int) (Result[Contact], error) {
	q := paging(page, perPage)
	if customerID > 0 {
		q.Set("customer_id", strconv.FormatInt(customerID, 10))
	}

	var body struct {
		Contacts []wireContact `json:"contacts"`
		Meta     wireMeta      `json:"meta"`
	}
	if err := c.get(ctx, "/contacts", q, &body); err != nil {
		return Result[Contact]{}, err
	}

	out := Result[Contact]{Items: make([]Contact, 0, len(body.Contacts))}
	for _, w := range body.Contacts {
		if trimmed, ok := w.trim(); ok {
			out.Items = append(out.Items, trimmed)
		}
	}
	out.Page = pageOf(body.Meta, page, perPage)
	return out, nil
}

type wireContact struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Firstname string `json:"firstname"`
	Lastname  string `json:"lastname"`
	Email     string `json:"email"`
	Phone     string `json:"phone"`
	Mobile    string `json:"mobile"`
	Title     string `json:"title"`
	Notes     string `json:"notes"`
}

// trim reports false for a record with nothing on it worth showing. Syncro
// keeps blank contacts around, and a list of empty rows is worse than a short
// list.
func (w wireContact) trim() (Contact, bool) {
	name := firstNonEmpty(w.Name, strings.TrimSpace(w.Firstname+" "+w.Lastname))
	if name == "" && w.Email == "" && w.Phone == "" && w.Mobile == "" {
		return Contact{}, false
	}
	return Contact{
		ID:     w.ID,
		Name:   firstNonEmpty(name, w.Email),
		Email:  w.Email,
		Phone:  w.Phone,
		Mobile: w.Mobile,
		Title:  w.Title,
		Notes:  truncate(w.Notes, 300),
	}, true
}
