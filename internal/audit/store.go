package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store handles audit log database operations.
type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Log records an audit entry. Fire-and-forget: logs errors instead of returning them.
func (s *Store) Log(ctx context.Context, action, entityType, entityID string, details any) {
	var detailsJSON []byte
	if details != nil {
		var err error
		detailsJSON, err = json.Marshal(details)
		if err != nil {
			slog.Error("audit: marshal details", "error", err)
			return
		}
	}

	_, err := s.pool.Exec(ctx,
		`INSERT INTO audit_log (action, entity_type, entity_id, details) VALUES ($1, $2, $3, $4)`,
		action, entityType, entityID, detailsJSON)
	if err != nil {
		slog.Error("audit: insert", "error", err, "action", action, "entity_type", entityType, "entity_id", entityID)
	}
}

// List returns paginated audit log entries, optionally filtered by entity_type and action.
func (s *Store) List(ctx context.Context, limit, offset int, entityType, action string) ([]Entry, int, error) {
	whereClauses := []string{}
	args := []any{}
	argIdx := 1

	if entityType != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("entity_type = $%d", argIdx))
		args = append(args, entityType)
		argIdx++
	}
	if action != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("action = $%d", argIdx))
		args = append(args, action)
		argIdx++
	}

	where := ""
	if len(whereClauses) > 0 {
		where = "WHERE " + strings.Join(whereClauses, " AND ")
	}

	args = append(args, limit, offset)
	query := fmt.Sprintf(
		`SELECT id, action, entity_type, entity_id, details, created_at, COUNT(*) OVER() AS total_count
		 FROM audit_log %s ORDER BY created_at DESC LIMIT $%d OFFSET $%d`,
		where, argIdx, argIdx+1,
	)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("query audit_log: %w", err)
	}
	defer rows.Close()

	var entries []Entry
	var total int
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.Action, &e.EntityType, &e.EntityID, &e.Details, &e.CreatedAt, &total); err != nil {
			return nil, 0, fmt.Errorf("scan audit entry: %w", err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate audit_log: %w", err)
	}

	return entries, total, nil
}

// ListByEntity returns audit log entries for a specific entity.
func (s *Store) ListByEntity(ctx context.Context, entityType, entityID string, limit int) ([]Entry, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, action, entity_type, entity_id, details, created_at
		 FROM audit_log WHERE entity_type = $1 AND entity_id = $2
		 ORDER BY created_at DESC LIMIT $3`,
		entityType, entityID, limit)
	if err != nil {
		return nil, fmt.Errorf("query audit_log by entity: %w", err)
	}
	defer rows.Close()

	var entries []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.Action, &e.EntityType, &e.EntityID, &e.Details, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan audit entry: %w", err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate audit_log: %w", err)
	}

	return entries, nil
}
