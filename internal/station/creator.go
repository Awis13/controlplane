package station

import (
	"context"
	"fmt"
	"log/slog"
)

// CreatorStore defines the store operations needed by Creator.
type CreatorStore interface {
	Create(ctx context.Context, req CreateStationRequest) (*Station, error)
	GetByTenantID(ctx context.Context, tenantID string) (*Station, error)
}

// Creator implements provisioner.StationCreator using a station store.
type Creator struct {
	store CreatorStore
}

// NewCreator creates a new station Creator.
func NewCreator(store CreatorStore) *Creator {
	return &Creator{store: store}
}

// AutoCreateStation creates a station record for a newly provisioned tenant.
// Idempotent: if a station for this tenant already exists, returns nil.
func (c *Creator) AutoCreateStation(ctx context.Context, tenantID, name, subdomain, ownerID, caddyDomain string) error {
	// Проверяем, существует ли уже станция для этого тенанта
	existing, err := c.store.GetByTenantID(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("check existing station: %w", err)
	}
	if existing != nil {
		slog.Info("station already exists for tenant, skipping create", "tenant_id", tenantID, "station_id", existing.ID)
		return nil
	}

	streamURL := fmt.Sprintf("https://%s.%s", subdomain, caddyDomain)

	var ownerPtr *string
	if ownerID != "" {
		ownerPtr = &ownerID
	}

	_, err = c.store.Create(ctx, CreateStationRequest{
		Name:      name,
		Slug:      subdomain,
		StreamURL: streamURL,
		TenantID:  &tenantID,
		OwnerID:   ownerPtr,
		IsPublic:  true,
	})
	if err != nil {
		return fmt.Errorf("create station: %w", err)
	}
	return nil
}
