package station

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"controlplane/internal/tenant"
)

// StationStatus holds live status data fetched from a tenant's dashboard API.
type StationStatus struct {
	IsOnline       bool      `json:"is_online"`
	ListenersCount int       `json:"listeners_count"`
	NowPlaying     string    `json:"now_playing"`
	BPM            float64   `json:"bpm"`
	FetchedAt      time.Time `json:"fetched_at"`
}

// PollerTenantLister provides active tenants for polling.
type PollerTenantLister interface {
	ListPollable(ctx context.Context) ([]tenant.PollableTenant, error)
}

// PollerStationUpdater updates station online status in the database.
type PollerStationUpdater interface {
	SetOnline(ctx context.Context, tenantID string, online bool) error
}

// Poller polls active tenant dashboard APIs to collect live station status.
type Poller struct {
	tenantLister    PollerTenantLister
	stationStore    PollerStationUpdater
	interval        time.Duration
	httpClient      *http.Client
	cache           sync.Map // key: tenantID (string), value: *StationStatus
	wg              sync.WaitGroup
	skipIPCheck     bool // только для тестов — пропускает SSRF-валидацию
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

// WithSkipIPCheck disables IP validation (used for testing with httptest servers).
func (p *Poller) WithSkipIPCheck() {
	p.skipIPCheck = true
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
	p.wg.Add(1)
	defer p.wg.Done()

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

// Wait blocks until the polling goroutine has exited.
func (p *Poller) Wait() {
	p.wg.Wait()
}

// PollOnce runs a single poll cycle (exposed for testing).
func (p *Poller) PollOnce(ctx context.Context) {
	p.poll(ctx)
}

func (p *Poller) poll(ctx context.Context) {
	start := time.Now()

	tenants, err := p.tenantLister.ListPollable(ctx)
	if err != nil {
		slog.Error("poller: list active tenants", "error", err)
		return
	}

	for _, t := range tenants {
		if ctx.Err() != nil {
			return
		}

		// SSRF protection: валидируем IP перед запросом
		if !p.skipIPCheck && !isAllowedIP(t.LXCIP) {
			slog.Warn("poller: skipping tenant with disallowed IP", "tenant_id", t.ID, "ip", t.LXCIP)
			continue
		}

		status := p.fetchStatus(ctx, t)
		p.cache.Store(t.ID, status)

		// Update is_online in DB (best-effort)
		if err := p.stationStore.SetOnline(ctx, t.ID, status.IsOnline); err != nil {
			slog.Debug("poller: update is_online", "tenant_id", t.ID, "error", err)
		}
	}

	elapsed := time.Since(start)
	if elapsed > p.interval {
		slog.Warn("poll cycle exceeded interval", "elapsed", elapsed, "interval", p.interval)
	}
}

// isAllowedIP validates that the IP is safe to poll.
// Only allows private 10.x.x.x addresses (tenant LXC network).
// Rejects loopback, link-local, unspecified, and non-10.x addresses.
func isAllowedIP(ipStr string) bool {
	// Может содержать порт (в тестах) — извлекаем только IP
	host, _, err := net.SplitHostPort(ipStr)
	if err != nil {
		// Нет порта — используем как есть
		host = ipStr
	}

	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}

	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return false
	}

	// Разрешаем только 10.0.0.0/8 (тенанты на 10.10.10.0/24)
	_, private10, _ := net.ParseCIDR("10.0.0.0/8")
	return private10.Contains(ip)
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

func (p *Poller) fetchStatus(ctx context.Context, t tenant.PollableTenant) *StationStatus {
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
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&data); err != nil {
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
