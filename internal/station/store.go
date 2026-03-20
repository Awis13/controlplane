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

const stationColumns = `id, name, slug, genre, description, artwork_url, stream_url, owner_id, tenant_id, is_public, is_online, created_at, updated_at`

func scanStation(row pgx.Row) (*Station, error) {
	var s Station
	err := row.Scan(&s.ID, &s.Name, &s.Slug, &s.Genre, &s.Description,
		&s.ArtworkURL, &s.StreamURL, &s.OwnerID, &s.TenantID, &s.IsPublic, &s.IsOnline,
		&s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// ListPublic returns public stations with search, filtering, sorting and pagination.
func (s *Store) ListPublic(ctx context.Context, p ListPublicParams) ([]Station, int, error) {
	whereClauses := []string{"is_public = true"}
	args := []any{}
	argIdx := 1

	if p.Query != "" {
		whereClauses = append(whereClauses, fmt.Sprintf(
			"(name ILIKE $%d OR genre ILIKE $%d OR description ILIKE $%d)", argIdx, argIdx, argIdx))
		args = append(args, "%"+p.Query+"%")
		argIdx++
	}
	if p.Genre != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("genre = $%d", argIdx))
		args = append(args, p.Genre)
		argIdx++
	}

	where := "WHERE " + strings.Join(whereClauses, " AND ")

	orderBy := "ORDER BY name"
	switch p.Sort {
	case "newest":
		orderBy = "ORDER BY created_at DESC"
	case "online_first":
		orderBy = "ORDER BY is_online DESC, name"
	}
	// "listeners" sort is done post-query in handler after poller enrichment

	// Count total
	var total int
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM stations %s", where)
	if err := s.pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count public stations: %w", err)
	}

	// Fetch page
	args = append(args, p.Limit, p.Offset)
	query := fmt.Sprintf("SELECT %s FROM stations %s %s LIMIT $%d OFFSET $%d",
		stationColumns, where, orderBy, argIdx, argIdx+1)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("query public stations: %w", err)
	}
	defer rows.Close()

	var stations []Station
	for rows.Next() {
		st, err := scanStation(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan station: %w", err)
		}
		stations = append(stations, *st)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate stations: %w", err)
	}

	return stations, total, nil
}

// ListGenres returns distinct genre values from public stations.
func (s *Store) ListGenres(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT DISTINCT genre FROM stations WHERE is_public = true AND genre != '' ORDER BY genre`)
	if err != nil {
		return nil, fmt.Errorf("query genres: %w", err)
	}
	defer rows.Close()

	var genres []string
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			return nil, fmt.Errorf("scan genre: %w", err)
		}
		genres = append(genres, g)
	}
	return genres, rows.Err()
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

// GetByTenantID returns a station by its tenant_id.
func (s *Store) GetByTenantID(ctx context.Context, tenantID string) (*Station, error) {
	st, err := scanStation(s.pool.QueryRow(ctx,
		`SELECT `+stationColumns+` FROM stations WHERE tenant_id = $1`, tenantID))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("query station by tenant: %w", err)
	}
	return st, nil
}

// Create inserts a new station and returns it.
func (s *Store) Create(ctx context.Context, req CreateStationRequest) (*Station, error) {
	st, err := scanStation(s.pool.QueryRow(ctx,
		`INSERT INTO stations (name, slug, genre, description, artwork_url, stream_url, owner_id, tenant_id, is_public)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		 RETURNING `+stationColumns,
		req.Name, req.Slug, req.Genre, req.Description, req.ArtworkURL, req.StreamURL,
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
	if req.StreamURL != nil {
		setClauses = append(setClauses, fmt.Sprintf("stream_url = $%d", argIdx))
		args = append(args, *req.StreamURL)
		argIdx++
	}
	if req.IsPublic != nil {
		setClauses = append(setClauses, fmt.Sprintf("is_public = $%d", argIdx))
		args = append(args, *req.IsPublic)
		argIdx++
	}
	if req.IsOnline != nil {
		setClauses = append(setClauses, fmt.Sprintf("is_online = $%d", argIdx))
		args = append(args, *req.IsOnline)
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

// SetOnline updates the is_online flag for a station identified by tenant_id.
func (s *Store) SetOnline(ctx context.Context, tenantID string, online bool) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE stations SET is_online = $2 WHERE tenant_id = $1`,
		tenantID, online)
	if err != nil {
		return fmt.Errorf("set station online: %w", err)
	}
	return nil
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
