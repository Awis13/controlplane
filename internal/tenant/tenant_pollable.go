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
// It is the same tenant set the route reconciler sees — see listActiveWithIP —
// projected onto the two columns the poller needs.
func (s *Store) ListPollable(ctx context.Context) ([]PollableTenant, error) {
	rows, err := s.listActiveWithIP(ctx)
	if err != nil {
		return nil, fmt.Errorf("list pollable tenants: %w", err)
	}
	return toPollableTenants(rows), nil
}

// toPollableTenants projects the shared rows onto what the poller needs.
func toPollableTenants(rows []activeWithIP) []PollableTenant {
	var tenants []PollableTenant
	for _, r := range rows {
		tenants = append(tenants, PollableTenant{ID: r.ID, LXCIP: r.LXCIP})
	}
	return tenants
}
