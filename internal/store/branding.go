package store

import (
	"context"
	"fmt"
	"time"
)

// Branding is what an operator may rename and recolour.
type Branding struct {
	Name     string `json:"name"`
	Tagline  string `json:"tagline"`
	Mark     string `json:"mark"`
	Accent   string `json:"accent"`
	LogoType string `json:"logo_type,omitempty"`
	// HasLogo rather than the bytes: the settings form only needs to know
	// whether one is set, and shipping a few hundred kilobytes of PNG inside a
	// JSON body on every page load would be a strange way to serve an image.
	HasLogo   bool      `json:"has_logo"`
	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy string    `json:"updated_by"`
}

func (db *DB) Branding(ctx context.Context) (Branding, error) {
	var b Branding
	var logo []byte
	err := db.pool.QueryRow(ctx, `
		SELECT name, tagline, mark, accent, logo, logo_type, updated_at, updated_by
		FROM branding WHERE id`).Scan(
		&b.Name, &b.Tagline, &b.Mark, &b.Accent, &logo, &b.LogoType, &b.UpdatedAt, &b.UpdatedBy)
	if err != nil {
		return Branding{}, fmt.Errorf("store: read branding: %w", err)
	}
	b.HasLogo = len(logo) > 0
	return b, nil
}

// Logo returns the stored image and its content type.
func (db *DB) Logo(ctx context.Context) ([]byte, string, error) {
	var logo []byte
	var kind string
	err := db.pool.QueryRow(ctx, `SELECT logo, logo_type FROM branding WHERE id`).Scan(&logo, &kind)
	if err != nil {
		return nil, "", fmt.Errorf("store: read logo: %w", err)
	}
	if len(logo) == 0 {
		return nil, "", ErrNotFound
	}
	return logo, kind, nil
}

// SetBranding writes everything except the logo, which has its own path
// because it is bytes rather than a form field.
func (db *DB) SetBranding(ctx context.Context, b Branding, by string) error {
	_, err := db.pool.Exec(ctx, `
		UPDATE branding SET name = $1, tagline = $2, mark = $3, accent = $4,
			updated_at = now(), updated_by = $5
		WHERE id`, b.Name, b.Tagline, b.Mark, b.Accent, by)
	if err != nil {
		return fmt.Errorf("store: write branding: %w", err)
	}
	return nil
}

// SetLogo stores an image, or clears it when given nothing.
func (db *DB) SetLogo(ctx context.Context, image []byte, kind, by string) error {
	_, err := db.pool.Exec(ctx, `
		UPDATE branding SET logo = $1, logo_type = $2, updated_at = now(), updated_by = $3
		WHERE id`, image, kind, by)
	if err != nil {
		return fmt.Errorf("store: write logo: %w", err)
	}
	return nil
}
