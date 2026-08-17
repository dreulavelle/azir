package syncro

// FromCustomerForTest exposes the side-detection rule to the package's tests
// without making it part of the public surface.
func FromCustomerForTest(cm Comment) bool { return fromCustomer(cm) }

// LinkForTest exposes the link a client would stamp on a ticket, so the URL
// construction can be checked without a live account to fetch one from.
func LinkForTest(c *Client, t Ticket) string { return c.withLink(t).URL }
