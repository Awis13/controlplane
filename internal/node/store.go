package node

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrInsufficientCapacity is returned when a node does not have enough RAM.
var ErrInsufficientCapacity = errors.New("insufficient capacity on node")

// ErrNoUpdate is returned when an update request has no fields to update.
var ErrNoUpdate = errors.New("no fields to update")

// ErrRAMBelowAllocated is returned when total_ram_mb would be set below allocated_ram_mb.
var ErrRAMBelowAllocated = errors.New("total_ram_mb cannot be less than allocated_ram_mb")

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

func (s *Store) List(ctx context.Context) ([]Node, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, tailscale_ip, proxmox_url, total_ram_mb, allocated_ram_mb, status, created_at, updated_at
		 FROM nodes ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("query nodes: %w", err)
	}
	defer rows.Close()

	var nodes []Node
	for rows.Next() {
		var n Node
		if err := rows.Scan(&n.ID, &n.Name, &n.TailscaleIP, &n.ProxmoxURL,
			&n.TotalRAMMB, &n.AllocatedRAMMB, &n.Status, &n.CreatedAt, &n.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan node: %w", err)
		}
		nodes = append(nodes, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate nodes: %w", err)
	}

	return nodes, nil
}

// ListPaginated returns a paginated list of nodes.
func (s *Store) ListPaginated(ctx context.Context, limit, offset int) ([]Node, int, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, tailscale_ip, proxmox_url, total_ram_mb, allocated_ram_mb, status, created_at, updated_at,
		        COUNT(*) OVER() AS total_count
		 FROM nodes ORDER BY created_at DESC LIMIT $1 OFFSET $2`,
		limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("query nodes: %w", err)
	}
	defer rows.Close()

	var nodes []Node
	var total int
	for rows.Next() {
		var n Node
		if err := rows.Scan(&n.ID, &n.Name, &n.TailscaleIP, &n.ProxmoxURL,
			&n.TotalRAMMB, &n.AllocatedRAMMB, &n.Status, &n.CreatedAt, &n.UpdatedAt, &total); err != nil {
			return nil, 0, fmt.Errorf("scan node: %w", err)
		}
		nodes = append(nodes, n)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate nodes: %w", err)
	}

	return nodes, total, nil
}

func (s *Store) GetByID(ctx context.Context, id string) (*Node, error) {
	var n Node
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, tailscale_ip, proxmox_url, total_ram_mb, allocated_ram_mb, status, created_at, updated_at
		 FROM nodes WHERE id = $1`, id).
		Scan(&n.ID, &n.Name, &n.TailscaleIP, &n.ProxmoxURL,
			&n.TotalRAMMB, &n.AllocatedRAMMB, &n.Status, &n.CreatedAt, &n.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("query node: %w", err)
	}
	return &n, nil
}

func (s *Store) Create(ctx context.Context, req CreateNodeRequest) (*Node, error) {
	var n Node
	err := s.pool.QueryRow(ctx,
		`INSERT INTO nodes (name, tailscale_ip, proxmox_url, api_token_encrypted, total_ram_mb)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING id, name, tailscale_ip, proxmox_url, total_ram_mb, allocated_ram_mb, status, created_at, updated_at`,
		req.Name, req.TailscaleIP, req.ProxmoxURL, req.APIToken, req.TotalRAMMB).
		Scan(&n.ID, &n.Name, &n.TailscaleIP, &n.ProxmoxURL,
			&n.TotalRAMMB, &n.AllocatedRAMMB, &n.Status, &n.CreatedAt, &n.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("insert node: %w", err)
	}
	return &n, nil
}

// Update applies partial updates to a node. Only non-nil fields are updated.
// If TotalRAMMB is being reduced, it must not go below allocated_ram_mb.
func (s *Store) Update(ctx context.Context, id string, req UpdateNodeRequest) (*Node, error) {
	setClauses := []string{}
	args := []any{}
	argIdx := 1

	// Track the arg index of total_ram_mb for the WHERE constraint
	var ramArgIdx int

	if req.Status != nil {
		setClauses = append(setClauses, fmt.Sprintf("status = $%d", argIdx))
		args = append(args, *req.Status)
		argIdx++
	}
	if req.TotalRAMMB != nil {
		ramArgIdx = argIdx
		setClauses = append(setClauses, fmt.Sprintf("total_ram_mb = $%d", argIdx))
		args = append(args, *req.TotalRAMMB)
		argIdx++
	}
	if req.ProxmoxURL != nil {
		setClauses = append(setClauses, fmt.Sprintf("proxmox_url = $%d", argIdx))
		args = append(args, *req.ProxmoxURL)
		argIdx++
	}
	if req.APIToken != nil {
		setClauses = append(setClauses, fmt.Sprintf("api_token_encrypted = $%d", argIdx))
		args = append(args, *req.APIToken)
		argIdx++
	}

	if len(setClauses) == 0 {
		return nil, ErrNoUpdate
	}

	// Build WHERE clause: always match by ID, optionally guard RAM
	var whereExtra string
	if ramArgIdx > 0 {
		whereExtra = fmt.Sprintf(" AND allocated_ram_mb <= $%d", ramArgIdx)
	}

	args = append(args, id)
	query := fmt.Sprintf(
		`UPDATE nodes SET %s WHERE id = $%d%s
		 RETURNING id, name, tailscale_ip, proxmox_url, total_ram_mb, allocated_ram_mb, status, created_at, updated_at`,
		strings.Join(setClauses, ", "), argIdx, whereExtra,
	)

	var n Node
	err := s.pool.QueryRow(ctx, query, args...).
		Scan(&n.ID, &n.Name, &n.TailscaleIP, &n.ProxmoxURL,
			&n.TotalRAMMB, &n.AllocatedRAMMB, &n.Status, &n.CreatedAt, &n.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			if req.TotalRAMMB != nil {
				// Check if node exists to distinguish not-found from RAM constraint
				existing, _ := s.GetByID(ctx, id)
				if existing != nil {
					return nil, ErrRAMBelowAllocated
				}
			}
			return nil, nil
		}
		return nil, fmt.Errorf("update node: %w", err)
	}
	return &n, nil
}

// CountTenants returns the number of non-deleted tenants on a node.
func (s *Store) CountTenants(ctx context.Context, nodeID string) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM tenants WHERE node_id = $1 AND status NOT IN ('deleted')`,
		nodeID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count tenants on node: %w", err)
	}
	return count, nil
}

func (s *Store) GetEncryptedTokenByID(ctx context.Context, id string) (string, error) {
	var token string
	err := s.pool.QueryRow(ctx,
		`SELECT api_token_encrypted FROM nodes WHERE id = $1`, id).
		Scan(&token)
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", nil
		}
		return "", fmt.Errorf("query node token: %w", err)
	}
	return token, nil
}

func (s *Store) Delete(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM nodes WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete node: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// GetLeastLoaded returns the active node with the most available RAM
// that has at least requiredMB free. Returns nil if no suitable node found.
func (s *Store) GetLeastLoaded(ctx context.Context, requiredMB int) (*Node, error) {
	var n Node
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, tailscale_ip, proxmox_url, total_ram_mb, allocated_ram_mb, status, created_at, updated_at
		 FROM nodes
		 WHERE status = 'active' AND (total_ram_mb - allocated_ram_mb) >= $1
		 ORDER BY (total_ram_mb - allocated_ram_mb) DESC
		 LIMIT 1`, requiredMB).
		Scan(&n.ID, &n.Name, &n.TailscaleIP, &n.ProxmoxURL,
			&n.TotalRAMMB, &n.AllocatedRAMMB, &n.Status, &n.CreatedAt, &n.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("query least loaded node: %w", err)
	}
	return &n, nil
}

// ReserveRAM atomically increments allocated_ram_mb if capacity is available.
// Returns ErrInsufficientCapacity if not enough RAM.
func (s *Store) ReserveRAM(ctx context.Context, nodeID string, ramMB int) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE nodes SET allocated_ram_mb = allocated_ram_mb + $2
		 WHERE id = $1 AND total_ram_mb - allocated_ram_mb >= $2`,
		nodeID, ramMB)
	if err != nil {
		return fmt.Errorf("reserve ram: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrInsufficientCapacity
	}
	return nil
}

// ReleaseRAM decrements allocated_ram_mb (floored at 0).
func (s *Store) ReleaseRAM(ctx context.Context, nodeID string, ramMB int) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE nodes SET allocated_ram_mb = GREATEST(allocated_ram_mb - $2, 0) WHERE id = $1`,
		nodeID, ramMB)
	if err != nil {
		return fmt.Errorf("release ram: %w", err)
	}
	return nil
}
