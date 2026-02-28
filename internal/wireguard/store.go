package wireguard

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store выполняет операции с БД для WireGuard пиров.
type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// peerColumns — список колонок для всех запросов к wireguard_peers.
const peerColumns = `id, name, public_key, preshared_key_encrypted, wg_ip, allowed_ips, endpoint, type, tenant_id, enabled, created_at, updated_at`

// scanPeer сканирует одну строку в структуру Peer.
func scanPeer(row pgx.Row) (*Peer, error) {
	var p Peer
	err := row.Scan(&p.ID, &p.Name, &p.PublicKey, &p.PresharedKeyEncrypted,
		&p.WgIP, &p.AllowedIPs, &p.Endpoint, &p.Type, &p.TenantID,
		&p.Enabled, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// List возвращает все пиры, отсортированные по дате создания.
func (s *Store) List(ctx context.Context) ([]Peer, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+peerColumns+` FROM wireguard_peers ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("query wireguard_peers: %w", err)
	}
	defer rows.Close()

	var peers []Peer
	for rows.Next() {
		p, err := scanPeer(rows)
		if err != nil {
			return nil, fmt.Errorf("scan peer: %w", err)
		}
		peers = append(peers, *p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate peers: %w", err)
	}

	return peers, nil
}

// ListByType возвращает пиры указанного типа.
func (s *Store) ListByType(ctx context.Context, peerType string) ([]Peer, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+peerColumns+` FROM wireguard_peers WHERE type = $1 ORDER BY created_at DESC`, peerType)
	if err != nil {
		return nil, fmt.Errorf("query wireguard_peers by type: %w", err)
	}
	defer rows.Close()

	var peers []Peer
	for rows.Next() {
		p, err := scanPeer(rows)
		if err != nil {
			return nil, fmt.Errorf("scan peer: %w", err)
		}
		peers = append(peers, *p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate peers: %w", err)
	}

	return peers, nil
}

// GetByID возвращает пир по ID. Nil если не найден.
func (s *Store) GetByID(ctx context.Context, id string) (*Peer, error) {
	p, err := scanPeer(s.pool.QueryRow(ctx,
		`SELECT `+peerColumns+` FROM wireguard_peers WHERE id = $1`, id))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("query peer: %w", err)
	}
	return p, nil
}

// Create вставляет нового пира и возвращает его.
func (s *Store) Create(ctx context.Context, p *Peer) (*Peer, error) {
	result, err := scanPeer(s.pool.QueryRow(ctx,
		`INSERT INTO wireguard_peers (name, public_key, preshared_key_encrypted, wg_ip, allowed_ips, endpoint, type, tenant_id, enabled)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		 RETURNING `+peerColumns,
		p.Name, p.PublicKey, p.PresharedKeyEncrypted, p.WgIP, p.AllowedIPs,
		p.Endpoint, p.Type, p.TenantID, p.Enabled))
	if err != nil {
		return nil, fmt.Errorf("insert peer: %w", err)
	}
	return result, nil
}

// ErrNoUpdate возвращается когда нет полей для обновления.
var ErrNoUpdate = fmt.Errorf("no fields to update")

// Update применяет частичное обновление пира. Обновляются только non-nil поля.
func (s *Store) Update(ctx context.Context, id string, req UpdatePeerRequest) (*Peer, error) {
	setClauses := []string{}
	args := []any{}
	argIdx := 1

	if req.Name != nil {
		setClauses = append(setClauses, fmt.Sprintf("name = $%d", argIdx))
		args = append(args, *req.Name)
		argIdx++
	}
	if req.Endpoint != nil {
		setClauses = append(setClauses, fmt.Sprintf("endpoint = $%d", argIdx))
		args = append(args, *req.Endpoint)
		argIdx++
	}
	if req.Enabled != nil {
		setClauses = append(setClauses, fmt.Sprintf("enabled = $%d", argIdx))
		args = append(args, *req.Enabled)
		argIdx++
	}

	if len(setClauses) == 0 {
		return nil, ErrNoUpdate
	}

	args = append(args, id)
	query := fmt.Sprintf(
		"UPDATE wireguard_peers SET %s WHERE id = $%d RETURNING %s",
		strings.Join(setClauses, ", "), argIdx, peerColumns,
	)

	p, err := scanPeer(s.pool.QueryRow(ctx, query, args...))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("update peer: %w", err)
	}
	return p, nil
}

// Delete удаляет пир по ID.
func (s *Store) Delete(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM wireguard_peers WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete peer: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// SetEnabled устанавливает статус enabled для пира.
func (s *Store) SetEnabled(ctx context.Context, id string, enabled bool) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE wireguard_peers SET enabled = $2 WHERE id = $1`, id, enabled)
	if err != nil {
		return fmt.Errorf("set peer enabled: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// GetNextAvailableIP находит следующий свободный IP в указанной подсети.
// Формат subnet: "10.10.0.0/24". Пропускает .0 (сеть) и .1 (шлюз/хаб).
func (s *Store) GetNextAvailableIP(ctx context.Context, subnet string) (string, error) {
	_, ipNet, err := net.ParseCIDR(subnet)
	if err != nil {
		return "", fmt.Errorf("parse subnet: %w", err)
	}

	// Получаем все занятые IP
	rows, err := s.pool.Query(ctx, `SELECT wg_ip FROM wireguard_peers`)
	if err != nil {
		return "", fmt.Errorf("query existing IPs: %w", err)
	}
	defer rows.Close()

	usedIPs := make(map[string]bool)
	for rows.Next() {
		var ip string
		if err := rows.Scan(&ip); err != nil {
			return "", fmt.Errorf("scan IP: %w", err)
		}
		usedIPs[ip] = true
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("iterate IPs: %w", err)
	}

	// Перебираем IP в подсети начиная с .2 (0=сеть, 1=шлюз)
	ip := make(net.IP, len(ipNet.IP))
	copy(ip, ipNet.IP)

	for i := 2; i < 255; i++ {
		ip[len(ip)-1] = byte(i)
		candidate := ip.String()
		if !ipNet.Contains(ip) {
			break
		}
		if !usedIPs[candidate] {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("no available IPs in subnet %s", subnet)
}

// GetByTenantID возвращает пир, привязанный к тенанту. Nil если не найден.
func (s *Store) GetByTenantID(ctx context.Context, tenantID string) (*Peer, error) {
	p, err := scanPeer(s.pool.QueryRow(ctx,
		`SELECT `+peerColumns+` FROM wireguard_peers WHERE tenant_id = $1 LIMIT 1`, tenantID))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("query peer by tenant: %w", err)
	}
	return p, nil
}

// ListEnabled возвращает все включённые пиры.
func (s *Store) ListEnabled(ctx context.Context) ([]Peer, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+peerColumns+` FROM wireguard_peers WHERE enabled = true ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("query enabled peers: %w", err)
	}
	defer rows.Close()

	var peers []Peer
	for rows.Next() {
		p, err := scanPeer(rows)
		if err != nil {
			return nil, fmt.Errorf("scan peer: %w", err)
		}
		peers = append(peers, *p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate peers: %w", err)
	}

	return peers, nil
}
