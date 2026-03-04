package station

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// StationStatus holds live status data fetched from a tenant's dashboard API.
type StationStatus struct {
	IsOnline       bool      `json:"is_online"`
	ListenersCount int       `json:"listeners_count"`
	NowPlaying     string    `json:"now_playing"`
	BPM            float64   `json:"bpm"`
	FetchedAt      time.Time `json:"fetched_at"`
}

// PollableTenant is a lightweight struct with the fields the poller needs.
type PollableTenant struct {
	ID    string
	LXCIP string
}

// PollerTenantLister provides active tenants for polling.
type PollerTenantLister interface {
	ListPollable(ctx context.Context) ([]PollableTenant, error)
}

// PollerStationUpdater updates station online status in the database.
type PollerStationUpdater interface {
	SetOnline(ctx context.Context, tenantID string, online bool) error
}

// Poller polls active tenant dashboard APIs to collect live station status.
type Poller struct {
	tenantLister PollerTenantLister
	stationStore PollerStationUpdater
	interval     time.Duration
	httpClient   *http.Client
	cache        sync.Map // key: tenantID (string), value: *StationStatus
}

// NewPoller creates a new station status poller.
func NewPoller(tenantLister PollerTenantLister, stationStore PollerStationUpdater, interval time.Duration) *Poller {
	return &Poller{
		tenantLister: tenantLister,
		stationStore: stationStore,
		interval:     interval,
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// WithHTTPClient sets a custom HTTP client (used for testing).
func (p *Poller) WithHTTPClient(c *http.Client) {
	p.httpClient = c
}

// GetStatus returns the cached status for a tenant, or nil if not available.
func (p *Poller) GetStatus(tenantID string) *StationStatus {
	val, ok := p.cache.Load(tenantID)
	if !ok {
		return nil
	}
	status, _ := val.(*StationStatus)
	return status
}

// Run starts the polling loop. Blocks until ctx is cancelled.
func (p *Poller) Run(ctx context.Context) {
	slog.Info("station poller started", "interval", p.interval)

	// Poll immediately on start, then on interval.
	p.poll(ctx)

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("station poller stopped")
			return
		case <-ticker.C:
			p.poll(ctx)
		}
	}
}

// PollOnce runs a single poll cycle (exposed for testing).
func (p *Poller) PollOnce(ctx context.Context) {
	p.poll(ctx)
}

func (p *Poller) poll(ctx context.Context) {
	tenants, err := p.tenantLister.ListPollable(ctx)
	if err != nil {
		slog.Error("poller: list active tenants", "error", err)
		return
	}

	for _, t := range tenants {
		if ctx.Err() != nil {
			return
		}
		status := p.fetchStatus(ctx, t)
		p.cache.Store(t.ID, status)

		// Update is_online in DB (best-effort)
		if err := p.stationStore.SetOnline(ctx, t.ID, status.IsOnline); err != nil {
			slog.Debug("poller: update is_online", "tenant_id", t.ID, "error", err)
		}
	}
}

// dashboardStatusResponse represents the JSON response from /api/status.
type dashboardStatusResponse struct {
	Audio struct {
		Title string `json:"title"`
	} `json:"audio"`
	Icecast struct {
		Listeners int `json:"listeners"`
	} `json:"icecast"`
	StreamMode struct {
		Mode string `json:"mode"`
	} `json:"streamMode"`
	BPM struct {
		Current float64 `json:"current"`
	} `json:"bpm"`
}

func (p *Poller) fetchStatus(ctx context.Context, t PollableTenant) *StationStatus {
	url := fmt.Sprintf("http://%s:80/api/status", t.LXCIP)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return &StationStatus{FetchedAt: time.Now()}
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return &StationStatus{FetchedAt: time.Now()}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return &StationStatus{FetchedAt: time.Now()}
	}

	var data dashboardStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		slog.Debug("poller: decode response", "tenant_id", t.ID, "error", err)
		return &StationStatus{FetchedAt: time.Now()}
	}

	isOnline := data.StreamMode.Mode == "live"

	return &StationStatus{
		IsOnline:       isOnline,
		ListenersCount: data.Icecast.Listeners,
		NowPlaying:     data.Audio.Title,
		BPM:            data.BPM.Current,
		FetchedAt:      time.Now(),
	}
}
