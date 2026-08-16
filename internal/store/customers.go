package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
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

	c := Customer{
		ID:          uuid.New(),
		DisplayName: displayName,
		CreatedAt:   time.Now().UTC(),
		Identities:  []Identity{},
	}
	_, err := db.write.ExecContext(ctx,
		`INSERT INTO customers (id, display_name, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		c.ID.String(), c.DisplayName, nowString(), nowString())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return Customer{}, fmt.Errorf("store: a customer named %q already exists", displayName)
		}
		return Customer{}, fmt.Errorf("store: create customer: %w", err)
	}
	return c, nil
}

// GetCustomer returns one customer with its identities.
func (db *DB) GetCustomer(ctx context.Context, id uuid.UUID) (Customer, error) {
	var (
		c         Customer
		idStr     string
		createdAt string
	)
	err := db.read.QueryRowContext(ctx,
		`SELECT id, display_name, created_at FROM customers WHERE id = ?`, id.String(),
	).Scan(&idStr, &c.DisplayName, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Customer{}, ErrNotFound
	}
	if err != nil {
		return Customer{}, fmt.Errorf("store: get customer: %w", err)
	}

	c.ID, _ = uuid.Parse(idStr)
	c.CreatedAt = parseTime(createdAt)

	c.Identities, err = db.identitiesFor(ctx, id)
	if err != nil {
		return Customer{}, err
	}
	return c, nil
}

// ListCustomers returns every customer, without identities.
func (db *DB) ListCustomers(ctx context.Context) ([]Customer, error) {
	rows, err := db.read.QueryContext(ctx,
		`SELECT id, display_name, created_at FROM customers ORDER BY display_name`)
	if err != nil {
		return nil, fmt.Errorf("store: list customers: %w", err)
	}
	defer rows.Close()

	out := []Customer{}
	for rows.Next() {
		var c Customer
		var idStr, createdAt string
		if err := rows.Scan(&idStr, &c.DisplayName, &createdAt); err != nil {
			return nil, err
		}
		c.ID, _ = uuid.Parse(idStr)
		c.CreatedAt = parseTime(createdAt)
		c.Identities = []Identity{}
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
	_, err := db.write.ExecContext(ctx,
		`INSERT INTO customer_identities (customer_id, plugin, external_id, created_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT (plugin, external_id) DO UPDATE SET customer_id = excluded.customer_id`,
		customerID.String(), plugin, externalID, nowString())
	if err != nil {
		return fmt.Errorf("store: link identity: %w", err)
	}
	return nil
}

// ResolveIdentity finds the customer an external record belongs to. This is
// how a Syncro ticket becomes an Azir customer without Syncro defining one.
func (db *DB) ResolveIdentity(ctx context.Context, plugin, externalID string) (Customer, error) {
	var idStr string
	err := db.read.QueryRowContext(ctx,
		`SELECT customer_id FROM customer_identities WHERE plugin = ? AND external_id = ?`,
		plugin, externalID,
	).Scan(&idStr)
	if errors.Is(err, sql.ErrNoRows) {
		return Customer{}, ErrNotFound
	}
	if err != nil {
		return Customer{}, fmt.Errorf("store: resolve identity: %w", err)
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		return Customer{}, fmt.Errorf("store: corrupt customer id in identity row")
	}
	return db.GetCustomer(ctx, id)
}

func (db *DB) identitiesFor(ctx context.Context, customerID uuid.UUID) ([]Identity, error) {
	rows, err := db.read.QueryContext(ctx,
		`SELECT plugin, external_id, created_at FROM customer_identities
		 WHERE customer_id = ? ORDER BY plugin`, customerID.String())
	if err != nil {
		return nil, fmt.Errorf("store: identities: %w", err)
	}
	defer rows.Close()

	out := []Identity{}
	for rows.Next() {
		var i Identity
		var createdAt string
		if err := rows.Scan(&i.Plugin, &i.ExternalID, &createdAt); err != nil {
			return nil, err
		}
		i.CreatedAt = parseTime(createdAt)
		out = append(out, i)
	}
	return out, rows.Err()
}
