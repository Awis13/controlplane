package tenant

import (
	"context"
	"fmt"
)

// PollableTenant is a lightweight struct for the station poller.
type PollableTenant struct {
	ID    string
	LXCIP string
}

// ListPollable returns active tenants with LXC IPs for station status polling.
func (s *Store) ListPollable(ctx context.Context) ([]PollableTenant, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, lxc_ip FROM tenants
		 WHERE status = 'active' AND lxc_ip IS NOT NULL AND lxc_ip != ''`)
	if err != nil {
		return nil, fmt.Errorf("query pollable tenants: %w", err)
	}
	defer rows.Close()

	var tenants []PollableTenant
	for rows.Next() {
		var t PollableTenant
		if err := rows.Scan(&t.ID, &t.LXCIP); err != nil {
			return nil, fmt.Errorf("scan pollable tenant: %w", err)
		}
		tenants = append(tenants, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pollable tenants: %w", err)
	}

	return tenants, nil
}
