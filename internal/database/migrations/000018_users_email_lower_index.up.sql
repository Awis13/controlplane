-- Logins look users up with lower(email) = lower($1) so that rows written
-- before addresses were normalized are still reachable. That predicate cannot
-- use the plain index on email, turning the unauthenticated login path into a
-- sequential scan.
--
-- Non-unique on purpose: a unique index here would be the real guarantee of one
-- account per address, but it fails to create if the table already holds rows
-- differing only in case. That needs a data audit and a dedupe first.
CREATE INDEX IF NOT EXISTS idx_users_email_lower ON users (lower(email));
