package salon

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dronm/ds/v4"
)

type PostgresRegistry struct{ db ds.Provider }

func NewPostgresRegistry(db ds.Provider) *PostgresRegistry {
	return &PostgresRegistry{db: db}
}

const columns = "company_id, slug, name, is_active, session_version"

func (r *PostgresRegistry) List(ctx context.Context) ([]Entry, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("salon database is not configured")
	}
	lease, server, err := r.db.GetPrimary(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire salon database: %w", err)
	}
	defer r.db.Release(lease, server)
	rows, err := lease.Conn().Query(ctx, `SELECT `+columns+` FROM public.salons WHERE is_active=true ORDER BY sort_order, name, company_id`)
	if err != nil {
		return nil, fmt.Errorf("list salons: %w", err)
	}
	defer rows.Close()
	items := make([]Entry, 0)
	for rows.Next() {
		var item Entry
		if err := scan(rows, &item); err != nil {
			return nil, fmt.Errorf("read salon list: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read salon list: %w", err)
	}
	return items, nil
}

func (r *PostgresRegistry) FindBySlug(ctx context.Context, slug string) (Entry, error) {
	if !ValidSlug(slug) {
		return Entry{}, ErrSalonNotFound
	}
	return r.find(ctx, `SELECT `+columns+` FROM public.salons WHERE slug=$1`, slug)
}

func (r *PostgresRegistry) FindByCompanyID(ctx context.Context, companyID int64) (Entry, error) {
	if companyID <= 0 {
		return Entry{}, ErrSalonNotFound
	}
	return r.find(ctx, `SELECT `+columns+` FROM public.salons WHERE company_id=$1`, companyID)
}

func (r *PostgresRegistry) find(ctx context.Context, query string, args ...any) (Entry, error) {
	if r == nil || r.db == nil {
		return Entry{}, errors.New("salon database is not configured")
	}
	lease, server, err := r.db.GetPrimary(ctx)
	if err != nil {
		return Entry{}, fmt.Errorf("acquire salon database: %w", err)
	}
	defer r.db.Release(lease, server)
	var item Entry
	err = scan(lease.Conn().QueryRow(ctx, query, args...), &item)
	if errors.Is(err, ds.ErrNoRows) {
		return Entry{}, ErrSalonNotFound
	}
	if err != nil {
		return Entry{}, fmt.Errorf("read salon: %w", err)
	}
	return item, nil
}

func scan(row ds.Row, item *Entry) error {
	return row.Scan(&item.CompanyID, &item.Slug, &item.Name, &item.IsActive, &item.SessionVersion)
}

// Upsert creates or renames the public entry without re-enabling a disabled one.
func (r *PostgresRegistry) Upsert(ctx context.Context, companyID int64, slug, name string) (Entry, error) {
	slug, name = strings.TrimSpace(slug), strings.TrimSpace(name)
	if err := Validate(companyID, slug, name); err != nil {
		return Entry{}, err
	}
	return r.find(ctx, `INSERT INTO public.salons (company_id, slug, name) VALUES ($1,$2,$3)
		ON CONFLICT (company_id) DO UPDATE SET slug=EXCLUDED.slug, name=EXCLUDED.name
		RETURNING `+columns, companyID, slug, name)
}

func (r *PostgresRegistry) SetActive(ctx context.Context, companyID int64, active bool) error {
	if companyID <= 0 {
		return errors.New("positive YClients company ID is required")
	}
	_, err := r.find(ctx, `UPDATE public.salons SET is_active=$2 WHERE company_id=$1 RETURNING `+columns, companyID, active)
	return err
}
