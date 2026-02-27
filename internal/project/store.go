package project

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNoUpdate is returned when an update request has no fields to update.
var ErrNoUpdate = errors.New("no fields to update")

// Store handles project database operations.
type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

const projectColumns = `id, name, template_id, ports, stripe_price_id, health_path, ram_mb, created_at, updated_at`

func (s *Store) List(ctx context.Context) ([]Project, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+projectColumns+` FROM projects ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("query projects: %w", err)
	}
	defer rows.Close()

	var projects []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Name, &p.TemplateID, &p.Ports,
			&p.StripePriceID, &p.HealthPath, &p.RAMMB, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		projects = append(projects, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate projects: %w", err)
	}

	return projects, nil
}

// ListPaginated returns a paginated list of projects.
func (s *Store) ListPaginated(ctx context.Context, limit, offset int) ([]Project, int, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+projectColumns+`, COUNT(*) OVER() AS total_count
		 FROM projects ORDER BY created_at DESC LIMIT $1 OFFSET $2`,
		limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("query projects: %w", err)
	}
	defer rows.Close()

	var projects []Project
	var total int
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Name, &p.TemplateID, &p.Ports,
			&p.StripePriceID, &p.HealthPath, &p.RAMMB, &p.CreatedAt, &p.UpdatedAt, &total); err != nil {
			return nil, 0, fmt.Errorf("scan project: %w", err)
		}
		projects = append(projects, p)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate projects: %w", err)
	}

	return projects, total, nil
}

func (s *Store) GetByID(ctx context.Context, id string) (*Project, error) {
	var p Project
	err := s.pool.QueryRow(ctx,
		`SELECT `+projectColumns+` FROM projects WHERE id = $1`, id).
		Scan(&p.ID, &p.Name, &p.TemplateID, &p.Ports,
			&p.StripePriceID, &p.HealthPath, &p.RAMMB, &p.CreatedAt, &p.UpdatedAt)
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
		 RETURNING `+projectColumns,
		req.Name, req.TemplateID, req.Ports, req.StripePriceID, req.HealthPath, req.RAMMB).
		Scan(&p.ID, &p.Name, &p.TemplateID, &p.Ports,
			&p.StripePriceID, &p.HealthPath, &p.RAMMB, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("insert project: %w", err)
	}
	return &p, nil
}

// Update applies partial updates to a project. Only non-nil fields are updated.
func (s *Store) Update(ctx context.Context, id string, req UpdateProjectRequest) (*Project, error) {
	setClauses := []string{}
	args := []any{}
	argIdx := 1

	if req.Name != nil {
		setClauses = append(setClauses, fmt.Sprintf("name = $%d", argIdx))
		args = append(args, *req.Name)
		argIdx++
	}
	if req.TemplateID != nil {
		setClauses = append(setClauses, fmt.Sprintf("template_id = $%d", argIdx))
		args = append(args, *req.TemplateID)
		argIdx++
	}
	if req.Ports != nil {
		setClauses = append(setClauses, fmt.Sprintf("ports = $%d", argIdx))
		args = append(args, *req.Ports)
		argIdx++
	}
	if req.StripePriceID != nil {
		setClauses = append(setClauses, fmt.Sprintf("stripe_price_id = $%d", argIdx))
		args = append(args, *req.StripePriceID)
		argIdx++
	}
	if req.HealthPath != nil {
		setClauses = append(setClauses, fmt.Sprintf("health_path = $%d", argIdx))
		args = append(args, *req.HealthPath)
		argIdx++
	}
	if req.RAMMB != nil {
		setClauses = append(setClauses, fmt.Sprintf("ram_mb = $%d", argIdx))
		args = append(args, *req.RAMMB)
		argIdx++
	}

	if len(setClauses) == 0 {
		return nil, ErrNoUpdate
	}

	args = append(args, id)
	query := fmt.Sprintf(
		`UPDATE projects SET %s WHERE id = $%d RETURNING %s`,
		strings.Join(setClauses, ", "), argIdx, projectColumns,
	)

	var p Project
	err := s.pool.QueryRow(ctx, query, args...).
		Scan(&p.ID, &p.Name, &p.TemplateID, &p.Ports,
			&p.StripePriceID, &p.HealthPath, &p.RAMMB, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("update project: %w", err)
	}
	return &p, nil
}

func (s *Store) Delete(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM projects WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete project: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// CountTenants returns the number of non-deleted tenants using this project.
func (s *Store) CountTenants(ctx context.Context, projectID string) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM tenants WHERE project_id = $1 AND status NOT IN ('deleted')`,
		projectID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count tenants for project: %w", err)
	}
	return count, nil
}
