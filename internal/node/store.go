package node

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrInsufficientCapacity is returned when a node does not have enough RAM.
var ErrInsufficientCapacity = errors.New("insufficient capacity on node")

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
