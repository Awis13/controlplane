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

// tenantColumns is the list of columns selected in all tenant queries.
const tenantColumns = `id, name, project_id, node_id, lxc_id, subdomain, status, error_message, stripe_subscription_id, created_at, updated_at`

// scanTenant scans a single row into a Tenant struct.
func scanTenant(row pgx.Row) (*Tenant, error) {
	var t Tenant
	err := row.Scan(&t.ID, &t.Name, &t.ProjectID, &t.NodeID, &t.LXCID,
		&t.Subdomain, &t.Status, &t.ErrorMessage, &t.StripeSubscriptionID, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *Store) List(ctx context.Context) ([]Tenant, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+tenantColumns+` FROM tenants ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("query tenants: %w", err)
	}
	defer rows.Close()

	var tenants []Tenant
	for rows.Next() {
		t, err := scanTenant(rows)
		if err != nil {
			return nil, fmt.Errorf("scan tenant: %w", err)
		}
		tenants = append(tenants, *t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tenants: %w", err)
	}

	return tenants, nil
}

func (s *Store) GetByID(ctx context.Context, id string) (*Tenant, error) {
	t, err := scanTenant(s.pool.QueryRow(ctx,
		`SELECT `+tenantColumns+` FROM tenants WHERE id = $1`, id))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("query tenant: %w", err)
	}
	return t, nil
}

func (s *Store) Create(ctx context.Context, req CreateTenantRequest) (*Tenant, error) {
	t, err := scanTenant(s.pool.QueryRow(ctx,
		`INSERT INTO tenants (name, project_id, node_id, subdomain)
		 VALUES ($1, $2, $3, $4)
		 RETURNING `+tenantColumns,
		req.Name, req.ProjectID, req.NodeID, req.Subdomain))
	if err != nil {
		return nil, fmt.Errorf("insert tenant: %w", err)
	}
	return t, nil
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

// ErrStateConflict is returned when a status transition is invalid (row was already transitioned).
var ErrStateConflict = fmt.Errorf("tenant state conflict")

// SetActive marks a tenant as active and records its LXC ID.
// Only transitions from 'provisioning' status.
func (s *Store) SetActive(ctx context.Context, id string, lxcID int) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE tenants SET status = 'active', lxc_id = $2, error_message = NULL
		 WHERE id = $1 AND status = 'provisioning'`,
		id, lxcID)
	if err != nil {
		return fmt.Errorf("set tenant active: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrStateConflict
	}
	return nil
}

// SetError marks a tenant as errored with a message.
// Allowed from 'provisioning' or 'deleting' status.
func (s *Store) SetError(ctx context.Context, id string, errMsg string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE tenants SET status = 'error', error_message = $2
		 WHERE id = $1 AND status IN ('provisioning', 'deleting')`,
		id, errMsg)
	if err != nil {
		return fmt.Errorf("set tenant error: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrStateConflict
	}
	return nil
}

// SetDeleting atomically transitions a tenant to 'deleting' status.
// Only transitions from 'active' or 'error' — acts as a compare-and-swap
// to prevent concurrent delete requests from double-deprovisioning.
func (s *Store) SetDeleting(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE tenants SET status = 'deleting'
		 WHERE id = $1 AND status IN ('active', 'error')`,
		id)
	if err != nil {
		return fmt.Errorf("set tenant deleting: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrStateConflict
	}
	return nil
}

// SetDeleted marks a tenant as deleted.
// Only transitions from 'deleting' status.
func (s *Store) SetDeleted(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE tenants SET status = 'deleted'
		 WHERE id = $1 AND status = 'deleting'`,
		id)
	if err != nil {
		return fmt.Errorf("set tenant deleted: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrStateConflict
	}
	return nil
}
