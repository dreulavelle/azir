package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Customer is Azir's own entity. External systems map onto it; it is not
// defined by any of them. A customer with no identities is valid.
type Customer struct {
	ID          uuid.UUID  `json:"id"`
	DisplayName string     `json:"display_name"`
	CreatedAt   time.Time  `json:"created_at"`
	Identities  []Identity `json:"identities"`
}

// Identity links a customer to a record in an external system.
type Identity struct {
	Plugin     string    `json:"plugin"`
	ExternalID string    `json:"external_id"`
	CreatedAt  time.Time `json:"created_at"`
}

// CreateCustomer adds a customer. Display names are unique case-insensitively,
// because two "Acme Dental" rows accumulating separate memory is worse than a
// rejected insert.
func (db *DB) CreateCustomer(ctx context.Context, displayName string) (Customer, error) {
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		return Customer{}, errors.New("store: display name is required")
	}

	c := Customer{ID: uuid.New(), DisplayName: displayName, Identities: []Identity{}}
	err := db.pool.QueryRow(ctx,
		`INSERT INTO customers (id, display_name) VALUES ($1, $2) RETURNING created_at`,
		c.ID, c.DisplayName,
	).Scan(&c.CreatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "customers_display_name_key") {
			return Customer{}, fmt.Errorf("store: a customer named %q already exists", displayName)
		}
		return Customer{}, fmt.Errorf("store: create customer: %w", err)
	}
	return c, nil
}

// GetCustomer returns one customer with its identities.
func (db *DB) GetCustomer(ctx context.Context, id uuid.UUID) (Customer, error) {
	var c Customer
	err := db.pool.QueryRow(ctx,
		`SELECT id, display_name, created_at FROM customers WHERE id = $1`, id,
	).Scan(&c.ID, &c.DisplayName, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Customer{}, ErrNotFound
	}
	if err != nil {
		return Customer{}, fmt.Errorf("store: get customer: %w", err)
	}

	c.Identities, err = db.identitiesFor(ctx, id)
	if err != nil {
		return Customer{}, err
	}
	return c, nil
}

// ListCustomers returns every customer with the identities each is known by.
//
// Hydrated in one extra query rather than left empty: the type promises
// identities and a caller matching a customer to the id a connected system
// knows them by has no other way to do it. Returning an empty slice looked like
// "this customer is not linked" and was indistinguishable from the truth.
func (db *DB) ListCustomers(ctx context.Context) ([]Customer, error) {
	rows, err := db.pool.Query(ctx,
		`SELECT id, display_name, created_at FROM customers ORDER BY display_name`)
	if err != nil {
		return nil, fmt.Errorf("store: list customers: %w", err)
	}
	defer rows.Close()

	out := []Customer{}
	at := map[uuid.UUID]int{}
	for rows.Next() {
		c := Customer{Identities: []Identity{}}
		if err := rows.Scan(&c.ID, &c.DisplayName, &c.CreatedAt); err != nil {
			return nil, err
		}
		at[c.ID] = len(out)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}

	// One query for every identity rather than one per customer: a list of two
	// hundred customers should not be two hundred round trips.
	links, err := db.pool.Query(ctx,
		`SELECT customer_id, plugin, external_id, created_at
		 FROM customer_identities ORDER BY plugin, external_id`)
	if err != nil {
		return nil, fmt.Errorf("store: list customer identities: %w", err)
	}
	defer links.Close()

	for links.Next() {
		var owner uuid.UUID
		var i Identity
		if err := links.Scan(&owner, &i.Plugin, &i.ExternalID, &i.CreatedAt); err != nil {
			return nil, err
		}
		if idx, ok := at[owner]; ok {
			out[idx].Identities = append(out[idx].Identities, i)
		}
	}
	return out, links.Err()
}

// SearchCustomers finds customers by fuzzy name, backed by the trigram index.
// The agent resolves a name it read in a ticket to a customer this way, so it
// never needs to be told an identifier.
// firstCustomers is the start of the list, by name, for a picker nobody has
// typed into yet.
func (db *DB) firstCustomers(ctx context.Context, limit int) ([]Customer, error) {
	rows, err := db.pool.Query(ctx,
		`SELECT id, display_name, created_at FROM customers ORDER BY display_name LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: first customers: %w", err)
	}
	defer rows.Close()

	out := []Customer{}
	for rows.Next() {
		c := Customer{Identities: []Identity{}}
		if err := rows.Scan(&c.ID, &c.DisplayName, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (db *DB) SearchCustomers(ctx context.Context, query string, limit int) ([]Customer, error) {
	query = strings.TrimSpace(query)
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	// Nothing typed yet is a real state, not a request for the whole table.
	// A picker opens on it, and an MSP with two thousand customers should get
	// the first screenful rather than all of them.
	if query == "" {
		return db.firstCustomers(ctx, limit)
	}

	rows, err := db.pool.Query(ctx, `
		SELECT id, display_name, created_at
		FROM customers
		WHERE display_name % $1 OR display_name ILIKE '%' || $1 || '%'
		ORDER BY similarity(display_name, $1) DESC, display_name
		LIMIT $2`, query, limit)
	if err != nil {
		return nil, fmt.Errorf("store: search customers: %w", err)
	}
	defer rows.Close()

	out := []Customer{}
	for rows.Next() {
		c := Customer{Identities: []Identity{}}
		if err := rows.Scan(&c.ID, &c.DisplayName, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// LinkIdentity maps an external record to a customer. The same external record
// cannot map to two customers, so identities cannot silently fork.
func (db *DB) LinkIdentity(ctx context.Context, customerID uuid.UUID, plugin, externalID string) error {
	if plugin == "" || externalID == "" {
		return errors.New("store: plugin and external id are required")
	}
	_, err := db.pool.Exec(ctx,
		`INSERT INTO customer_identities (customer_id, plugin, external_id)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (plugin, external_id) DO UPDATE SET customer_id = EXCLUDED.customer_id`,
		customerID, plugin, externalID)
	if err != nil {
		return fmt.Errorf("store: link identity: %w", err)
	}
	return nil
}

// ResolveIdentity finds the customer an external record belongs to. This is
// how a Syncro ticket becomes an Azir customer without Syncro defining one.
func (db *DB) ResolveIdentity(ctx context.Context, plugin, externalID string) (Customer, error) {
	var id uuid.UUID
	err := db.pool.QueryRow(ctx,
		`SELECT customer_id FROM customer_identities WHERE plugin = $1 AND external_id = $2`,
		plugin, externalID,
	).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Customer{}, ErrNotFound
	}
	if err != nil {
		return Customer{}, fmt.Errorf("store: resolve identity: %w", err)
	}
	return db.GetCustomer(ctx, id)
}

func (db *DB) identitiesFor(ctx context.Context, customerID uuid.UUID) ([]Identity, error) {
	rows, err := db.pool.Query(ctx,
		`SELECT plugin, external_id, created_at FROM customer_identities
		 WHERE customer_id = $1 ORDER BY plugin`, customerID)
	if err != nil {
		return nil, fmt.Errorf("store: identities: %w", err)
	}
	defer rows.Close()

	out := []Identity{}
	for rows.Next() {
		var i Identity
		if err := rows.Scan(&i.Plugin, &i.ExternalID, &i.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}
