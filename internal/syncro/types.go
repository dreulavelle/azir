// Package syncro is a read-only client for the Syncro MSP API.
//
// Everything it returns is trimmed rather than passed through. Vendor JSON is
// verbose and much of it is irrelevant to a technician's question, and every
// field that survives costs model context on a request that is already
// expensive. Deciding what matters is the plugin's job, not the model's.
package syncro

import "strings"

// Customer is the trimmed view of a Syncro customer.
type Customer struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Business  string `json:"business_name,omitempty"`
	Email     string `json:"email,omitempty"`
	Phone     string `json:"phone,omitempty"`
	Address   string `json:"address,omitempty"`
	Notes     string `json:"notes,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

// Ticket is the trimmed view of a Syncro ticket.
type Ticket struct {
	ID          int64  `json:"id"`
	Number      string `json:"number,omitempty"`
	Subject     string `json:"subject"`
	Status      string `json:"status,omitempty"`
	Priority    string `json:"priority,omitempty"`
	ProblemType string `json:"problem_type,omitempty"`
	CustomerID  int64  `json:"customer_id,omitempty"`
	Customer    string `json:"customer,omitempty"`
	AssignedTo  string `json:"assigned_to,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
	// Comments are only populated by a single-ticket fetch. A search returning
	// every comment for every hit would swamp the context window.
	Comments []Comment `json:"comments,omitempty"`
}

// Comment is one entry on a ticket thread.
type Comment struct {
	ID        int64  `json:"id"`
	Subject   string `json:"subject,omitempty"`
	Body      string `json:"body"`
	Hidden    bool   `json:"hidden"`
	TechName  string `json:"tech,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

// Asset is the trimmed view of a customer asset.
type Asset struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	AssetType   string `json:"asset_type,omitempty"`
	CustomerID  int64  `json:"customer_id,omitempty"`
	AssetSerial string `json:"serial,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
}

// Page describes where a result sits in a paginated set, so a caller knows
// whether it is looking at everything or at the first slice of something
// larger — a distinction a model will otherwise assume wrongly.
type Page struct {
	Page       int `json:"page"`
	PerPage    int `json:"per_page"`
	TotalPages int `json:"total_pages,omitempty"`
	TotalCount int `json:"total_count,omitempty"`
}

// Result wraps a list with its pagination.
type Result[T any] struct {
	Items []T  `json:"items"`
	Page  Page `json:"page"`
}

// --- wire types -------------------------------------------------------------
//
// These mirror Syncro's responses and exist only to be trimmed into the types
// above. Keeping them separate means a vendor field change is a compile error
// in one place rather than a silent shape change in model context.

type wireMeta struct {
	TotalPages int `json:"total_pages"`
	TotalCount int `json:"total_entries"`
	Page       int `json:"page"`
}

type wireCustomer struct {
	ID           int64  `json:"id"`
	Firstname    string `json:"firstname"`
	Lastname     string `json:"lastname"`
	FullName     string `json:"fullname"`
	BusinessName string `json:"business_name"`
	Email        string `json:"email"`
	Phone        string `json:"phone"`
	Mobile       string `json:"mobile"`
	Address      string `json:"address"`
	City         string `json:"city"`
	State        string `json:"state"`
	Zip          string `json:"zip"`
	Notes        string `json:"notes"`
	CreatedAt    string `json:"created_at"`
}

type wireTicket struct {
	ID          int64  `json:"id"`
	Number      any    `json:"number"`
	Subject     string `json:"subject"`
	Status      string `json:"status"`
	Priority    string `json:"priority"`
	ProblemType string `json:"problem_type"`
	CustomerID  int64  `json:"customer_id"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	Customer    *struct {
		BusinessName string `json:"business_name"`
		FullName     string `json:"fullname"`
	} `json:"customer"`
	User *struct {
		FullName string `json:"full_name"`
		Email    string `json:"email"`
	} `json:"user"`
	Comments []wireComment `json:"comments"`
}

type wireComment struct {
	ID        int64  `json:"id"`
	Subject   string `json:"subject"`
	Body      string `json:"body"`
	Hidden    bool   `json:"hidden"`
	TechName  string `json:"tech"`
	CreatedAt string `json:"created_at"`
}

type wireAsset struct {
	ID         int64          `json:"id"`
	Name       string         `json:"name"`
	AssetType  string         `json:"asset_type"`
	CustomerID int64          `json:"customer_id"`
	CreatedAt  string         `json:"created_at"`
	Properties map[string]any `json:"properties"`
}

// --- trimming ---------------------------------------------------------------

func (w wireCustomer) trim() Customer {
	name := w.FullName
	if name == "" {
		name = strings.TrimSpace(w.Firstname + " " + w.Lastname)
	}
	if name == "" {
		name = w.BusinessName
	}

	phone := w.Phone
	if phone == "" {
		phone = w.Mobile
	}

	return Customer{
		ID:        w.ID,
		Name:      name,
		Business:  w.BusinessName,
		Email:     w.Email,
		Phone:     phone,
		Address:   joinNonEmpty(", ", w.Address, w.City, w.State, w.Zip),
		Notes:     truncate(w.Notes, 500),
		CreatedAt: w.CreatedAt,
	}
}

func (w wireTicket) trim(withComments bool) Ticket {
	t := Ticket{
		ID:          w.ID,
		Number:      stringify(w.Number),
		Subject:     w.Subject,
		Status:      w.Status,
		Priority:    w.Priority,
		ProblemType: w.ProblemType,
		CustomerID:  w.CustomerID,
		CreatedAt:   w.CreatedAt,
		UpdatedAt:   w.UpdatedAt,
	}
	if w.Customer != nil {
		t.Customer = w.Customer.BusinessName
		if t.Customer == "" {
			t.Customer = w.Customer.FullName
		}
	}
	if w.User != nil {
		t.AssignedTo = w.User.FullName
	}
	if withComments {
		for _, c := range w.Comments {
			t.Comments = append(t.Comments, Comment{
				ID:        c.ID,
				Subject:   c.Subject,
				Body:      truncate(c.Body, 4000),
				Hidden:    c.Hidden,
				TechName:  c.TechName,
				CreatedAt: c.CreatedAt,
			})
		}
	}
	return t
}

func (w wireAsset) trim() Asset {
	a := Asset{
		ID:         w.ID,
		Name:       w.Name,
		AssetType:  w.AssetType,
		CustomerID: w.CustomerID,
		CreatedAt:  w.CreatedAt,
	}
	// Serial hides in a free-form properties bag whose keys vary by asset type.
	for _, key := range []string{"serial", "Serial", "serial_number", "Serial Number"} {
		if v, ok := w.Properties[key]; ok {
			if s := stringify(v); s != "" {
				a.AssetSerial = s
				break
			}
		}
	}
	return a
}
