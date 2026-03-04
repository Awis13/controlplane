package station

import (
	"context"
	"fmt"
)

// Creator implements provisioner.StationCreator using a station Store.
type Creator struct {
	store *Store
}

// NewCreator creates a new station Creator.
func NewCreator(store *Store) *Creator {
	return &Creator{store: store}
}

// AutoCreateStation creates a station record for a newly provisioned tenant.
func (c *Creator) AutoCreateStation(ctx context.Context, tenantID, name, subdomain, ownerID, caddyDomain string) error {
	streamURL := fmt.Sprintf("https://%s.%s", subdomain, caddyDomain)

	var ownerPtr *string
	if ownerID != "" {
		ownerPtr = &ownerID
	}

	_, err := c.store.Create(ctx, CreateStationRequest{
		Name:     name,
		Slug:     subdomain,
		StreamURL: streamURL,
		TenantID: &tenantID,
		OwnerID:  ownerPtr,
		IsPublic: true,
	})
	if err != nil {
		return fmt.Errorf("create station: %w", err)
	}
	return nil
}
