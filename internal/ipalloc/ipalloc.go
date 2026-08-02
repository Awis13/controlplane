// Package ipalloc allocates addresses out of a subnet.
//
// It exists because two stores grew their own copy of this: one for tenant
// container addresses and one for WireGuard peers. The copies disagreed about
// locking, and both walked only the last octet of the address, which silently
// capped any network wider than a /24 and allocated nothing at all in a subnet
// that does not start on a .0 boundary.
package ipalloc

import (
	"context"
	"fmt"
	"net/netip"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AdvisoryLockKey is the Postgres advisory lock every allocation takes.
//
// The value spells "LXCI" in ASCII and dates from when only tenant container
// addresses were allocated. It is a single global key rather than one derived
// from the subnet: allocations are rare, and one key means two callers
// allocating from overlapping subnets still serialize against each other,
// which a per-subnet key would not guarantee.
const AdvisoryLockKey int64 = 0x4C584349

// UsedLister reports the addresses already taken. It runs inside the
// allocating transaction, so it sees a consistent snapshot behind the lock.
type UsedLister func(ctx context.Context, tx pgx.Tx) (map[string]bool, error)

// Next returns the first free host address in cidr.
//
// Skipped, matching what both original implementations did for a /24: the
// network address, the address after it (the gateway or hub), and the
// broadcast address. Unlike them, the whole host range is walked, so a /16
// yields more than 253 addresses and a subnet such as 10.10.0.128/25 yields
// its own hosts rather than nothing.
func Next(cidr string, used map[string]bool) (string, error) {
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		return "", fmt.Errorf("parse cidr %q: %w", cidr, err)
	}
	if !prefix.Addr().Is4() {
		// Only IPv4 is allocated here. Refusing is better than walking an IPv6
		// host space that cannot be enumerated.
		return "", fmt.Errorf("cidr %q is not IPv4", cidr)
	}

	prefix = prefix.Masked()
	network := prefix.Addr()
	broadcast := lastAddr(prefix)

	// network, then the gateway, then the first usable host.
	candidate := network.Next().Next()
	for candidate.Less(broadcast) {
		if s := candidate.String(); !used[s] {
			return s, nil
		}
		candidate = candidate.Next()
	}

	return "", fmt.Errorf("no available IPs in %s", cidr)
}

// lastAddr returns the broadcast address of an IPv4 prefix: every host bit set.
func lastAddr(prefix netip.Prefix) netip.Addr {
	bytes := prefix.Addr().As4()
	hostBits := 32 - prefix.Bits()
	for i := 0; i < hostBits; i++ {
		bytes[3-i/8] |= 1 << (i % 8)
	}
	return netip.AddrFrom4(bytes)
}

// Allocate picks a free address under the advisory lock, so two callers
// allocating at the same time cannot choose the same one. listUsed supplies
// the addresses already taken, queried inside the same transaction.
func Allocate(ctx context.Context, pool *pgxpool.Pool, cidr string, listUsed UsedLister) (string, error) {
	// Fail before opening a transaction if the subnet is unusable.
	if _, err := netip.ParsePrefix(cidr); err != nil {
		return "", fmt.Errorf("parse cidr %q: %w", cidr, err)
	}

	var result string
	err := pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, AdvisoryLockKey); err != nil {
			return fmt.Errorf("advisory lock: %w", err)
		}

		used, err := listUsed(ctx, tx)
		if err != nil {
			return err
		}

		addr, err := Next(cidr, used)
		if err != nil {
			return err
		}
		result = addr
		return nil
	})
	if err != nil {
		return "", err
	}
	return result, nil
}

// CollectIPs reads a single-column result of addresses into a set. Both stores
// list their used addresses this way.
func CollectIPs(rows pgx.Rows) (map[string]bool, error) {
	defer rows.Close()

	used := make(map[string]bool)
	for rows.Next() {
		var ip string
		if err := rows.Scan(&ip); err != nil {
			return nil, fmt.Errorf("scan ip: %w", err)
		}
		used[ip] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate ips: %w", err)
	}
	return used, nil
}
