// Package syncro is a client for the Syncro MSP API: reading freely, and
// writing only through the two allowlisted paths in write.go.
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
	// Contact is who raised it, when the ticket names somebody.
	Contact *Contact `json:"contact,omitempty"`
	// URL is where a person opens this ticket in Syncro. Stamped by the client,
	// which knows the account's own address; a trimmed record has no idea where
	// it came from.
	URL string `json:"url,omitempty"`
	// Comments are only populated by a single-ticket fetch. A search returning
	// every comment for every hit would swamp the context window.
	Comments []Comment `json:"comments,omitempty"`
}

// Contact is a person at a customer: who to call about this.
//
// A customer record is a company and its main line. The person who actually
// reported the fault is somebody at that company, and reaching them is the
// thing a technician is trying to do when a ticket stalls.
type Contact struct {
	ID     int64  `json:"id,omitempty"`
	Name   string `json:"name"`
	Email  string `json:"email,omitempty"`
	Phone  string `json:"phone,omitempty"`
	Mobile string `json:"mobile,omitempty"`
	Title  string `json:"title,omitempty"`
	Notes  string `json:"notes,omitempty"`
}

// Comment is one entry on a ticket thread.
type Comment struct {
	ID      int64  `json:"id"`
	Subject string `json:"subject,omitempty"`
	Body    string `json:"body"`
	Hidden  bool   `json:"hidden"`

	// From is who wrote this: "customer" or "technician".
	//
	// Stated outright because it cannot be worked out from the fields around
	// it. Syncro stamps `tech` with the API key's owner on everything created
	// through the API, including the customer's own replies — so a reader
	// looking at TechName sees our name on their message. The only reliable
	// signal is Syncro's own subject-line convention, and expecting every
	// caller to know that is expecting them to get it wrong. The assistant in
	// particular has to know who said what: half a conversation read as if one
	// side wrote all of it produces a confident summary of the wrong thing.
	From string `json:"from"`

	// Internal marks a note the customer never saw. Distinct from From: a
	// technician writes both kinds, and drafting a reply to something the
	// customer cannot see is a mistake worth making impossible to make.
	Internal bool `json:"internal"`

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
// Page fields are never omitempty. A genuine zero — "this customer has no
// assets" — must be distinguishable from an absent field, or a model reading
// the result has to guess which it is.
type Page struct {
	Page       int `json:"page"`
	PerPage    int `json:"per_page"`
	TotalPages int `json:"total_pages"`
	TotalCount int `json:"total_count"`
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

	// Syncro sends the customer two different ways depending on the endpoint:
	// a list response leaves `customer` null and carries only this flattened
	// name, while a single-ticket fetch populates both. Reading only the
	// nested object leaves every search result without a customer name, which
	// is what the first real response revealed.
	CustomerName string `json:"customer_business_then_name"`
	Customer     *struct {
		BusinessName string `json:"business_name"`
		FullName     string `json:"fullname"`
	} `json:"customer"`

	// Who reported it. Absent on a ticket raised against the company rather
	// than a named person, which is why every field below is optional.
	ContactID int64 `json:"contact_id"`
	Contact   *struct {
		ID        int64  `json:"id"`
		Name      string `json:"name"`
		Firstname string `json:"firstname"`
		Lastname  string `json:"lastname"`
		Email     string `json:"email"`
		Phone     string `json:"phone"`
		Mobile    string `json:"mobile"`
		Title     string `json:"title"`
		Notes     string `json:"notes"`
	} `json:"contact"`

	// Null on an unassigned ticket. The populated shape is unverified against
	// a live assigned ticket, so the fallbacks below are deliberate.
	User *struct {
		FullName string `json:"full_name"`
		Fullname string `json:"fullname"`
		Name     string `json:"name"`
		Email    string `json:"email"`
	} `json:"user"`

	Comments []wireComment `json:"comments"`
}

type wireComment struct {
	ID       int64  `json:"id"`
	Subject  string `json:"subject"`
	Body     string `json:"body"`
	Hidden   bool   `json:"hidden"`
	TechName string `json:"tech"`
	// Syncro can store a comment as HTML. When it does, the plain preview is
	// the better thing to put in front of a model: markup is noise that costs
	// context and occasionally confuses a summariser.
	IsRichText bool   `json:"is_rich_text"`
	PlainText  string `json:"simple_text_preview"`
	CreatedAt  string `json:"created_at"`
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
	// The flattened name is the only field present on both endpoints, so it
	// is preferred; the nested object is a fallback for anything that omits it.
	t.Customer = w.CustomerName
	if t.Customer == "" && w.Customer != nil {
		t.Customer = w.Customer.BusinessName
		if t.Customer == "" {
			t.Customer = w.Customer.FullName
		}
	}
	if w.User != nil {
		t.AssignedTo = firstNonEmpty(w.User.FullName, w.User.Fullname, w.User.Name, w.User.Email)
	}
	if c := w.Contact; c != nil {
		name := firstNonEmpty(c.Name, strings.TrimSpace(c.Firstname+" "+c.Lastname), c.Email)
		// A contact with no way to reach it and no name is not a contact.
		if name != "" || c.Email != "" || c.Phone != "" || c.Mobile != "" {
			t.Contact = &Contact{
				ID: firstNonZero(c.ID, w.ContactID), Name: name, Email: c.Email,
				Phone: c.Phone, Mobile: c.Mobile, Title: c.Title,
				Notes: truncate(c.Notes, 300),
			}
		}
	}
	if withComments {
		for _, c := range w.Comments {
			t.Comments = append(t.Comments, c.trim())
		}
	}
	return t
}

// firstNonZero returns the first value that is not zero.
func firstNonZero(values ...int64) int64 {
	for _, v := range values {
		if v != 0 {
			return v
		}
	}
	return 0
}

// firstNonEmpty returns the first value that is not blank.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// trim turns one comment into the shape a caller gets, attribution included.
//
// Shared with the write path. Building the same struct by hand over there
// produced a comment that was hidden and simultaneously not internal, with
// nobody recorded as having said it — the same type meaning different things
// depending on which endpoint returned it.
func (c wireComment) trim() Comment {
	body := c.Body
	if c.IsRichText && c.PlainText != "" {
		body = c.PlainText
	}
	out := Comment{
		ID:        c.ID,
		Subject:   c.Subject,
		Body:      truncate(firstNonEmpty(body, c.PlainText), 4000),
		Hidden:    c.Hidden,
		Internal:  c.Hidden,
		TechName:  c.TechName,
		CreatedAt: c.CreatedAt,
	}
	// A caller should never have to know Syncro's subject-line convention to
	// work out who was speaking.
	out.From = "technician"
	if fromCustomer(out) {
		out.From = "customer"
	}
	return out
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
