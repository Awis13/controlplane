package caddy

import (
	"context"
	"fmt"
	"log/slog"
)

// TenantLister provides the list of active tenants with their IPs for route reconciliation.
type TenantLister interface {
	ListActiveWithIP(ctx context.Context) ([]TenantRoute, error)
}

// TenantRoute is a tenant subdomain + IP pair used for reconciliation.
type TenantRoute struct {
	Subdomain string
	LXCIP     string
}

// ReconcileResult holds the outcome of a reconciliation run.
type ReconcileResult struct {
	Success int
	Failed  int
}

// Reconcile ensures all active tenants with IPs have Caddy routes configured.
// It logs errors per-tenant but continues, returning the overall result.
func Reconcile(ctx context.Context, client *Client, lister TenantLister) (*ReconcileResult, error) {
	tenants, err := lister.ListActiveWithIP(ctx)
	if err != nil {
		return nil, fmt.Errorf("caddy: list active tenants: %w", err)
	}

	if len(tenants) == 0 {
		slog.Info("caddy: reconcile: no active tenants with IPs")
		return &ReconcileResult{}, nil
	}

	result := &ReconcileResult{}
	for _, t := range tenants {
		if err := client.AddRoute(ctx, t.Subdomain, t.LXCIP); err != nil {
			slog.Error("caddy: reconcile: failed to add route",
				"subdomain", t.Subdomain, "lxc_ip", t.LXCIP, "error", err)
			result.Failed++
		} else {
			slog.Info("caddy: reconcile: route added",
				"subdomain", t.Subdomain, "target", t.LXCIP)
			result.Success++
		}
	}

	return result, nil
}
