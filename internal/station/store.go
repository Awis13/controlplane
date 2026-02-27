package station

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

// Store handles station database operations.
type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

const stationColumns = `id, name, slug, genre, description, artwork_url, owner_id, tenant_id, is_public, created_at, updated_at`

func scanStation(row pgx.Row) (*Station, error) {
	var s Station
	err := row.Scan(&s.ID, &s.Name, &s.Slug, &s.Genre, &s.Description,
		&s.ArtworkURL, &s.OwnerID, &s.TenantID, &s.IsPublic, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// ListPublic returns public stations ordered by name.
func (s *Store) ListPublic(ctx context.Context) ([]Station, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+stationColumns+` FROM stations WHERE is_public = true ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("query public stations: %w", err)
	}
	defer rows.Close()

	var stations []Station
	for rows.Next() {
		st, err := scanStation(rows)
		if err != nil {
			return nil, fmt.Errorf("scan station: %w", err)
		}
		stations = append(stations, *st)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate stations: %w", err)
	}

	return stations, nil
}

// GetBySlug returns a single station by its slug.
func (s *Store) GetBySlug(ctx context.Context, slug string) (*Station, error) {
	st, err := scanStation(s.pool.QueryRow(ctx,
		`SELECT `+stationColumns+` FROM stations WHERE slug = $1`, slug))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("query station by slug: %w", err)
	}
	return st, nil
}

// GetByID returns a single station by its ID.
func (s *Store) GetByID(ctx context.Context, id string) (*Station, error) {
	st, err := scanStation(s.pool.QueryRow(ctx,
		`SELECT `+stationColumns+` FROM stations WHERE id = $1`, id))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("query station: %w", err)
	}
	return st, nil
}

// Create inserts a new station and returns it.
func (s *Store) Create(ctx context.Context, req CreateStationRequest) (*Station, error) {
	st, err := scanStation(s.pool.QueryRow(ctx,
		`INSERT INTO stations (name, slug, genre, description, artwork_url, owner_id, tenant_id, is_public)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 RETURNING `+stationColumns,
		req.Name, req.Slug, req.Genre, req.Description, req.ArtworkURL,
		req.OwnerID, req.TenantID, req.IsPublic))
	if err != nil {
		return nil, fmt.Errorf("insert station: %w", err)
	}
	return st, nil
}

// Update applies partial updates to a station. Only non-nil fields are updated.
func (s *Store) Update(ctx context.Context, id string, req UpdateStationRequest) (*Station, error) {
	setClauses := []string{}
	args := []any{}
	argIdx := 1

	if req.Name != nil {
		setClauses = append(setClauses, fmt.Sprintf("name = $%d", argIdx))
		args = append(args, *req.Name)
		argIdx++
	}
	if req.Slug != nil {
		setClauses = append(setClauses, fmt.Sprintf("slug = $%d", argIdx))
		args = append(args, *req.Slug)
		argIdx++
	}
	if req.Genre != nil {
		setClauses = append(setClauses, fmt.Sprintf("genre = $%d", argIdx))
		args = append(args, *req.Genre)
		argIdx++
	}
	if req.Description != nil {
		setClauses = append(setClauses, fmt.Sprintf("description = $%d", argIdx))
		args = append(args, *req.Description)
		argIdx++
	}
	if req.ArtworkURL != nil {
		setClauses = append(setClauses, fmt.Sprintf("artwork_url = $%d", argIdx))
		args = append(args, *req.ArtworkURL)
		argIdx++
	}
	if req.IsPublic != nil {
		setClauses = append(setClauses, fmt.Sprintf("is_public = $%d", argIdx))
		args = append(args, *req.IsPublic)
		argIdx++
	}

	if len(setClauses) == 0 {
		return nil, ErrNoUpdate
	}

	args = append(args, id)
	query := fmt.Sprintf(
		`UPDATE stations SET %s WHERE id = $%d RETURNING %s`,
		strings.Join(setClauses, ", "), argIdx, stationColumns,
	)

	st, err := scanStation(s.pool.QueryRow(ctx, query, args...))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("update station: %w", err)
	}
	return st, nil
}

// Delete removes a station by ID.
func (s *Store) Delete(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM stations WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete station: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}
