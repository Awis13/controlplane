package project

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store handles project database operations.
type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

func (s *Store) List(ctx context.Context) ([]Project, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, template_id, ports, stripe_price_id, health_path, ram_mb, created_at
		 FROM projects ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("query projects: %w", err)
	}
	defer rows.Close()

	var projects []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Name, &p.TemplateID, &p.Ports,
			&p.StripePriceID, &p.HealthPath, &p.RAMMB, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		projects = append(projects, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate projects: %w", err)
	}

	return projects, nil
}

func (s *Store) GetByID(ctx context.Context, id string) (*Project, error) {
	var p Project
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, template_id, ports, stripe_price_id, health_path, ram_mb, created_at
		 FROM projects WHERE id = $1`, id).
		Scan(&p.ID, &p.Name, &p.TemplateID, &p.Ports,
			&p.StripePriceID, &p.HealthPath, &p.RAMMB, &p.CreatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("query project: %w", err)
	}
	return &p, nil
}

func (s *Store) Create(ctx context.Context, req CreateProjectRequest) (*Project, error) {
	if req.Ports == nil {
		req.Ports = []int{}
	}
	if req.HealthPath == "" {
		req.HealthPath = "/api/health"
	}
	if req.RAMMB == 0 {
		req.RAMMB = 1536
	}

	var p Project
	err := s.pool.QueryRow(ctx,
		`INSERT INTO projects (name, template_id, ports, stripe_price_id, health_path, ram_mb)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING id, name, template_id, ports, stripe_price_id, health_path, ram_mb, created_at`,
		req.Name, req.TemplateID, req.Ports, req.StripePriceID, req.HealthPath, req.RAMMB).
		Scan(&p.ID, &p.Name, &p.TemplateID, &p.Ports,
			&p.StripePriceID, &p.HealthPath, &p.RAMMB, &p.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("insert project: %w", err)
	}
	return &p, nil
}
