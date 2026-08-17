package syncro

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/dreulavelle/azir/pkg/plugin"
)

// Writes are the mutating operations this plugin supports.
//
// Deliberately few. Azir is a place to work tickets, not a second Syncro: it
// can say something, move a ticket along, and put it in the right hands. It
// cannot create, delete, invoice or charge, because those either belong in
// Syncro or are irreversible enough that a second pair of eyes is the point.
const (
	writeCommentPath = "/tickets/%d/comment"
	writeTicketPath  = "/tickets/%d"
)

// CommentRequest posts a message to a ticket.
type CommentRequest struct {
	TicketID int64
	Subject  string
	Body     string
	// Hidden makes this an internal note the customer never sees.
	Hidden bool
	// DoNotEmail posts without notifying the customer. Ignored for hidden
	// comments, which are never emailed anyway.
	DoNotEmail bool
}

// PostComment adds a comment to a ticket.
//
// This is the write that matters: it is the difference between Azir drafting a
// reply and someone copying that reply into another tab.
func (c *Client) PostComment(ctx context.Context, req CommentRequest) (Comment, error) {
	if req.TicketID <= 0 {
		return Comment{}, plugin.Errorf("400", "a ticket id is required")
	}
	if strings.TrimSpace(req.Body) == "" {
		return Comment{}, plugin.Errorf("400", "a comment body is required")
	}

	// Syncro rejects a comment with a blank subject. Supplying one is kinder
	// than surfacing a validation error for a field most callers do not think
	// about, and the value says where the note came from.
	subject := strings.TrimSpace(req.Subject)
	if subject == "" {
		if req.Hidden {
			subject = "Internal note"
		} else {
			subject = "Reply"
		}
	}

	payload, err := json.Marshal(map[string]any{
		"subject":      subject,
		"body":         req.Body,
		"hidden":       req.Hidden,
		"do_not_email": req.DoNotEmail || req.Hidden,
	})
	if err != nil {
		return Comment{}, err
	}

	raw, err := c.http.Do(ctx, http.MethodPost,
		"/tickets/"+strconv.FormatInt(req.TicketID, 10)+"/comment",
		nil, bytes.NewReader(payload))
	if err != nil {
		return Comment{}, err
	}

	var body struct {
		Comment wireComment `json:"comment"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return Comment{}, plugin.Errorf("502", "syncro accepted the comment but returned an unreadable response")
	}

	return body.Comment.trim(), nil
}

// TicketUpdate changes a ticket's fields. Only the set ones are sent, so an
// unrelated field cannot be blanked by omission.
type TicketUpdate struct {
	TicketID int64
	Status   string
	// UserID assigns the ticket to a technician. Zero leaves it unchanged.
	UserID int64
	// CustomerID moves the ticket to a different customer. This is what a
	// PagerDuty ticket landing on a generic account needs.
	CustomerID int64
	Priority   string
}

// UpdateTicket applies a change and returns the ticket as Syncro now holds it.
func (c *Client) UpdateTicket(ctx context.Context, u TicketUpdate) (Ticket, error) {
	if u.TicketID <= 0 {
		return Ticket{}, plugin.Errorf("400", "a ticket id is required")
	}

	fields := map[string]any{}
	if u.Status != "" {
		// Syncro accepts any string as a status — verified by setting one to
		// "Not A Real Status" and having it stick. A typo would silently put a
		// ticket somewhere no filter or report will ever find it, so the list
		// this account actually defines is the authority.
		statuses, err := c.TicketStatuses(ctx)
		if err == nil && len(statuses) > 0 {
			match := ""
			for _, s := range statuses {
				if strings.EqualFold(s, u.Status) {
					match = s
					break
				}
			}
			if match == "" {
				return Ticket{}, plugin.Errorf("400",
					"%q is not a status this Syncro account defines; valid values are: %s",
					u.Status, strings.Join(statuses, ", "))
			}
			// Use the account's exact casing rather than whatever was typed.
			u.Status = match
		}
		fields["status"] = u.Status
	}
	if u.UserID > 0 {
		fields["user_id"] = u.UserID
	}
	if u.CustomerID > 0 {
		fields["customer_id"] = u.CustomerID
	}
	if u.Priority != "" {
		fields["priority"] = u.Priority
	}
	if len(fields) == 0 {
		return Ticket{}, plugin.Errorf("400", "nothing to change")
	}

	payload, err := json.Marshal(fields)
	if err != nil {
		return Ticket{}, err
	}

	raw, err := c.http.Do(ctx, http.MethodPut,
		"/tickets/"+strconv.FormatInt(u.TicketID, 10), nil, bytes.NewReader(payload))
	if err != nil {
		return Ticket{}, err
	}

	var body struct {
		Ticket wireTicket `json:"ticket"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return Ticket{}, plugin.Errorf("502", "syncro accepted the change but returned an unreadable response")
	}
	return body.Ticket.trim(false), nil
}

// Technician is someone a ticket can be assigned to.
type Technician struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email,omitempty"`
}

// Technicians lists who tickets can be assigned to, so an interface can offer
// names rather than asking someone to know a numeric id.
func (c *Client) Technicians(ctx context.Context) ([]Technician, error) {
	var body struct {
		Users []struct {
			ID       int64  `json:"id"`
			FullName string `json:"full_name"`
			Name     string `json:"name"`
			Email    string `json:"email"`
		} `json:"users"`
	}
	if err := c.get(ctx, "/users", nil, &body); err != nil {
		return nil, err
	}

	out := make([]Technician, 0, len(body.Users))
	for _, u := range body.Users {
		out = append(out, Technician{
			ID:    u.ID,
			Name:  firstNonEmpty(u.FullName, u.Name, u.Email),
			Email: u.Email,
		})
	}
	return out, nil
}

// TicketStatuses returns the statuses this Syncro account defines, so an
// interface offers real options rather than a guessed list.
func (c *Client) TicketStatuses(ctx context.Context) ([]string, error) {
	var body struct {
		Statuses []string `json:"ticket_status_list"`
	}
	if err := c.get(ctx, "/tickets/settings", nil, &body); err != nil {
		return nil, err
	}
	return body.Statuses, nil
}
