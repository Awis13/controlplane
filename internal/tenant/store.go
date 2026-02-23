package tenant

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store handles tenant database operations.
type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

func (s *Store) List(ctx context.Context) ([]Tenant, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, project_id, node_id, lxc_id, subdomain, status, stripe_subscription_id, created_at, updated_at
		 FROM tenants ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("query tenants: %w", err)
	}
	defer rows.Close()

	var tenants []Tenant
	for rows.Next() {
		var t Tenant
		if err := rows.Scan(&t.ID, &t.Name, &t.ProjectID, &t.NodeID, &t.LXCID,
			&t.Subdomain, &t.Status, &t.StripeSubscriptionID, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan tenant: %w", err)
		}
		tenants = append(tenants, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tenants: %w", err)
	}

	return tenants, nil
}

func (s *Store) GetByID(ctx context.Context, id string) (*Tenant, error) {
	var t Tenant
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, project_id, node_id, lxc_id, subdomain, status, stripe_subscription_id, created_at, updated_at
		 FROM tenants WHERE id = $1`, id).
		Scan(&t.ID, &t.Name, &t.ProjectID, &t.NodeID, &t.LXCID,
			&t.Subdomain, &t.Status, &t.StripeSubscriptionID, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("query tenant: %w", err)
	}
	return &t, nil
}

func (s *Store) Create(ctx context.Context, req CreateTenantRequest) (*Tenant, error) {
	var t Tenant
	err := s.pool.QueryRow(ctx,
		`INSERT INTO tenants (name, project_id, node_id, subdomain)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id, name, project_id, node_id, lxc_id, subdomain, status, stripe_subscription_id, created_at, updated_at`,
		req.Name, req.ProjectID, req.NodeID, req.Subdomain).
		Scan(&t.ID, &t.Name, &t.ProjectID, &t.NodeID, &t.LXCID,
			&t.Subdomain, &t.Status, &t.StripeSubscriptionID, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("insert tenant: %w", err)
	}
	return &t, nil
}

func (s *Store) Delete(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete tenant: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}
