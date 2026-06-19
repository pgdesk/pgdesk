-- Demo schema for the pgdesk basic example.
-- Load with: psql "$PGDESK_DSN" -f examples/basic/schema.sql

CREATE TYPE user_status AS ENUM ('active', 'suspended', 'pending');

CREATE TABLE IF NOT EXISTS users (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email       text        NOT NULL UNIQUE,
    full_name   text,
    status      user_status NOT NULL DEFAULT 'pending',
    is_admin    boolean     NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

COMMENT ON TABLE users IS 'Application user accounts';
COMMENT ON COLUMN users.email IS 'Primary contact email; must be unique';

-- Durable audit sink written inside each mutation's transaction (O4).
CREATE TABLE IF NOT EXISTS audit_log (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    actor_id    text,
    action      text        NOT NULL,
    resource    text        NOT NULL,
    action_name text,
    row_key     text,
    before_snap jsonb,
    after_snap  jsonb,
    source_ip   text,
    request_id  text,
    at          timestamptz NOT NULL
);

INSERT INTO users (email, full_name, status, is_admin)
VALUES
    ('ada@example.com',   'Ada Lovelace',   'active',    true),
    ('alan@example.com',  'Alan Turing',    'active',    false),
    ('grace@example.com', 'Grace Hopper',   'suspended', false)
ON CONFLICT (email) DO NOTHING;
