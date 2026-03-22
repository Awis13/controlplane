package tenant

import (
	"context"
	"fmt"
	"net"
	"strings"

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
const tenantColumns = `id, name, project_id, node_id, lxc_id, lxc_ip, subdomain, status, error_message, owner_id, stripe_subscription_id, stripe_customer_id, tier, dashboard_token, health_status, health_checked_at, created_at, updated_at`

// scanTenant scans a single row into a Tenant struct.
func scanTenant(row pgx.Row) (*Tenant, error) {
	var t Tenant
	err := row.Scan(&t.ID, &t.Name, &t.ProjectID, &t.NodeID, &t.LXCID, &t.LXCIP,
		&t.Subdomain, &t.Status, &t.ErrorMessage, &t.OwnerID, &t.StripeSubscriptionID,
		&t.StripeCustomerID, &t.Tier, &t.DashboardToken, &t.HealthStatus, &t.HealthCheckedAt, &t.CreatedAt, &t.UpdatedAt)
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

// ListPaginated returns a filtered, paginated list of tenants.
func (s *Store) ListPaginated(ctx context.Context, limit, offset int, status, nodeID, projectID string) ([]Tenant, int, error) {
	whereClauses := []string{}
	args := []any{}
	argIdx := 1

	if status != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("status = $%d", argIdx))
		args = append(args, status)
		argIdx++
	}
	if nodeID != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("node_id = $%d", argIdx))
		args = append(args, nodeID)
		argIdx++
	}
	if projectID != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("project_id = $%d", argIdx))
		args = append(args, projectID)
		argIdx++
	}

	where := ""
	if len(whereClauses) > 0 {
		where = "WHERE " + strings.Join(whereClauses, " AND ")
	}

	args = append(args, limit, offset)
	query := fmt.Sprintf(
		`SELECT %s, COUNT(*) OVER() AS total_count
		 FROM tenants %s ORDER BY created_at DESC LIMIT $%d OFFSET $%d`,
		tenantColumns, where, argIdx, argIdx+1,
	)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("query tenants: %w", err)
	}
	defer rows.Close()

	var tenants []Tenant
	var total int
	for rows.Next() {
		var t Tenant
		err := rows.Scan(&t.ID, &t.Name, &t.ProjectID, &t.NodeID, &t.LXCID, &t.LXCIP,
			&t.Subdomain, &t.Status, &t.ErrorMessage, &t.OwnerID, &t.StripeSubscriptionID,
			&t.StripeCustomerID, &t.Tier, &t.DashboardToken, &t.HealthStatus, &t.HealthCheckedAt, &t.CreatedAt, &t.UpdatedAt, &total)
		if err != nil {
			return nil, 0, fmt.Errorf("scan tenant: %w", err)
		}
		tenants = append(tenants, t)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate tenants: %w", err)
	}

	return tenants, total, nil
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

// CreateWithOwner inserts a new tenant with an owner_id (user-created tenants).
func (s *Store) CreateWithOwner(ctx context.Context, req CreateTenantRequest, ownerID string) (*Tenant, error) {
	t, err := scanTenant(s.pool.QueryRow(ctx,
		`INSERT INTO tenants (name, project_id, node_id, subdomain, owner_id)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING `+tenantColumns,
		req.Name, req.ProjectID, req.NodeID, req.Subdomain, ownerID))
	if err != nil {
		return nil, fmt.Errorf("insert tenant: %w", err)
	}
	return t, nil
}

// CountByOwnerID returns the number of non-deleted tenants belonging to a user.
func (s *Store) CountByOwnerID(ctx context.Context, ownerID string) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM tenants WHERE owner_id = $1 AND status NOT IN ('deleted')`,
		ownerID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count tenants by owner: %w", err)
	}
	return count, nil
}

// ListByOwnerID returns all non-deleted tenants belonging to a user.
func (s *Store) ListByOwnerID(ctx context.Context, ownerID string) ([]Tenant, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+tenantColumns+` FROM tenants
		 WHERE owner_id = $1 AND status NOT IN ('deleted')
		 ORDER BY created_at DESC`, ownerID)
	if err != nil {
		return nil, fmt.Errorf("query tenants by owner: %w", err)
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

// ErrNoUpdate is returned when an update request has no fields to update.
var ErrNoUpdate = fmt.Errorf("no fields to update")

// ErrStateConflict is returned when a status transition is invalid (row was already transitioned).
var ErrStateConflict = fmt.Errorf("tenant state conflict")

// Update applies partial updates to a tenant. Only non-nil fields are updated.
func (s *Store) Update(ctx context.Context, id string, req UpdateTenantRequest) (*Tenant, error) {
	setClauses := []string{}
	args := []any{}
	argIdx := 1

	if req.Name != nil {
		setClauses = append(setClauses, fmt.Sprintf("name = $%d", argIdx))
		args = append(args, *req.Name)
		argIdx++
	}
	if req.StripeSubscriptionID != nil {
		setClauses = append(setClauses, fmt.Sprintf("stripe_subscription_id = $%d", argIdx))
		args = append(args, *req.StripeSubscriptionID)
		argIdx++
	}
	if req.StripeCustomerID != nil {
		setClauses = append(setClauses, fmt.Sprintf("stripe_customer_id = $%d", argIdx))
		args = append(args, *req.StripeCustomerID)
		argIdx++
	}

	if len(setClauses) == 0 {
		return nil, ErrNoUpdate
	}

	args = append(args, id)
	query := fmt.Sprintf(
		"UPDATE tenants SET %s WHERE id = $%d RETURNING %s",
		strings.Join(setClauses, ", "), argIdx, tenantColumns,
	)

	t, err := scanTenant(s.pool.QueryRow(ctx, query, args...))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("update tenant: %w", err)
	}
	return t, nil
}

// SetSuspended transitions a tenant from 'active' to 'suspended'.
func (s *Store) SetSuspended(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE tenants SET status = 'suspended'
		 WHERE id = $1 AND status = 'active'`,
		id)
	if err != nil {
		return fmt.Errorf("set tenant suspended: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrStateConflict
	}
	return nil
}

// SetResumed transitions a tenant from 'suspended' to 'active'.
func (s *Store) SetResumed(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE tenants SET status = 'active'
		 WHERE id = $1 AND status = 'suspended'`,
		id)
	if err != nil {
		return fmt.Errorf("set tenant resumed: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrStateConflict
	}
	return nil
}

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
// Only transitions from 'active', 'error', or 'suspended' — acts as a compare-and-swap
// to prevent concurrent delete requests from double-deprovisioning.
func (s *Store) SetDeleting(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE tenants SET status = 'deleting'
		 WHERE id = $1 AND status IN ('active', 'error', 'suspended')`,
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

// SetLXCIP sets the LXC container IP for a tenant.
func (s *Store) SetLXCIP(ctx context.Context, id string, ip string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE tenants SET lxc_ip = $2 WHERE id = $1`,
		id, ip)
	if err != nil {
		return fmt.Errorf("set tenant lxc_ip: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// ActiveTenant is a lightweight struct for route reconciliation.
type ActiveTenant struct {
	Subdomain string
	LXCIP     string
}

// ListActiveWithIP returns all active tenants that have an lxc_ip set.
func (s *Store) ListActiveWithIP(ctx context.Context) ([]ActiveTenant, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT subdomain, lxc_ip FROM tenants
		 WHERE status = 'active' AND lxc_ip IS NOT NULL AND lxc_ip != ''`)
	if err != nil {
		return nil, fmt.Errorf("query active tenants with ip: %w", err)
	}
	defer rows.Close()

	var tenants []ActiveTenant
	for rows.Next() {
		var t ActiveTenant
		if err := rows.Scan(&t.Subdomain, &t.LXCIP); err != nil {
			return nil, fmt.Errorf("scan active tenant: %w", err)
		}
		tenants = append(tenants, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active tenants: %w", err)
	}

	return tenants, nil
}

// GetNextAvailableIP finds the next free IP in the given CIDR.
// Skips .0 (network) and .1 (gateway). Checks occupied IPs from active/provisioning tenants.
// Uses pg_advisory_xact_lock to prevent race conditions between concurrent provisioners.
func (s *Store) GetNextAvailableIP(ctx context.Context, cidr string) (string, error) {
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", fmt.Errorf("parse cidr: %w", err)
	}

	var result string
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		// Advisory lock prevents concurrent IP allocation races.
		// Key 0x4C584349 = "LXCI" — unique to IP allocation.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(0x4C584349)); err != nil {
			return fmt.Errorf("advisory lock: %w", err)
		}

		rows, err := tx.Query(ctx,
			`SELECT lxc_ip FROM tenants WHERE lxc_ip IS NOT NULL AND lxc_ip != '' AND status NOT IN ('deleted')`)
		if err != nil {
			return fmt.Errorf("query used ips: %w", err)
		}
		defer rows.Close()

		usedIPs := make(map[string]bool)
		for rows.Next() {
			var ip string
			if err := rows.Scan(&ip); err != nil {
				return fmt.Errorf("scan ip: %w", err)
			}
			usedIPs[ip] = true
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate ips: %w", err)
		}

		ip := make(net.IP, len(ipNet.IP))
		copy(ip, ipNet.IP)
		for i := 2; i < 255; i++ {
			ip[len(ip)-1] = byte(i)
			candidate := ip.String()
			if !ipNet.Contains(ip) {
				break
			}
			if !usedIPs[candidate] {
				result = candidate
				return nil
			}
		}
		return fmt.Errorf("no available IPs in %s", cidr)
	})
	if err != nil {
		return "", err
	}
	return result, nil
}

// SetHealthStatus sets the health status of a tenant.
func (s *Store) SetHealthStatus(ctx context.Context, id string, status string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE tenants SET health_status = $2, health_checked_at = now() WHERE id = $1`,
		id, status)
	if err != nil {
		return fmt.Errorf("set health status: %w", err)
	}
	return nil
}

// SetDashboardToken saves the dashboard token for a tenant.
func (s *Store) SetDashboardToken(ctx context.Context, id string, token string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE tenants SET dashboard_token = $2 WHERE id = $1`,
		id, token)
	if err != nil {
		return fmt.Errorf("set dashboard token: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// UpdateBilling updates the billing fields (stripe_customer_id, stripe_subscription_id, tier) for a tenant.
func (s *Store) UpdateBilling(ctx context.Context, tenantID, stripeCustomerID, stripeSubscriptionID, tier string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE tenants SET stripe_customer_id = $2, stripe_subscription_id = $3, tier = $4, updated_at = now()
		 WHERE id = $1`,
		tenantID, stripeCustomerID, stripeSubscriptionID, tier)
	if err != nil {
		return fmt.Errorf("update billing: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// BillingTenant is a lightweight struct for billing operations.
type BillingTenant struct {
	ID                   string  `json:"id"`
	Name                 string  `json:"name"`
	Tier                 string  `json:"tier"`
	StripeCustomerID     *string `json:"stripe_customer_id,omitempty"`
	StripeSubscriptionID *string `json:"stripe_subscription_id,omitempty"`
	OwnerID              *string `json:"owner_id,omitempty"`
}

// GetByStripeCustomerID returns a tenant by its Stripe customer ID.
func (s *Store) GetByStripeCustomerID(ctx context.Context, customerID string) (*BillingTenant, error) {
	var t BillingTenant
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, tier, stripe_customer_id, stripe_subscription_id, owner_id
		 FROM tenants WHERE stripe_customer_id = $1 AND status NOT IN ('deleted')
		 LIMIT 1`, customerID).
		Scan(&t.ID, &t.Name, &t.Tier, &t.StripeCustomerID, &t.StripeSubscriptionID, &t.OwnerID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("query tenant by stripe customer: %w", err)
	}
	return &t, nil
}

// GetBillingByOwnerID returns billing info for all non-deleted tenants belonging to a user.
func (s *Store) GetBillingByOwnerID(ctx context.Context, ownerID string) ([]BillingTenant, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, tier, stripe_customer_id, stripe_subscription_id, owner_id
		 FROM tenants
		 WHERE owner_id = $1 AND status NOT IN ('deleted')
		 ORDER BY created_at DESC`, ownerID)
	if err != nil {
		return nil, fmt.Errorf("query billing tenants by owner: %w", err)
	}
	defer rows.Close()

	var tenants []BillingTenant
	for rows.Next() {
		var t BillingTenant
		if err := rows.Scan(&t.ID, &t.Name, &t.Tier, &t.StripeCustomerID, &t.StripeSubscriptionID, &t.OwnerID); err != nil {
			return nil, fmt.Errorf("scan billing tenant: %w", err)
		}
		tenants = append(tenants, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate billing tenants: %w", err)
	}

	return tenants, nil
}
