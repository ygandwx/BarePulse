CREATE TABLE console_sessions (
 id text PRIMARY KEY, actor_id text NOT NULL REFERENCES users(id), server_id text NOT NULL REFERENCES servers(id),
 device_id text NOT NULL REFERENCES devices(id), binding_id text NOT NULL REFERENCES bindings(id),
 binding_version bigint NOT NULL, server_version bigint NOT NULL, device_version bigint NOT NULL,
 port integer NOT NULL CHECK(port BETWEEN 1 AND 16), generation bigint NOT NULL DEFAULT 1,
 state text NOT NULL CHECK(state IN ('queued','acquiring','active','protected','revoking','released','blocked')),
 protection_reason text NOT NULL DEFAULT '', error_code text NOT NULL DEFAULT '', expires_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX console_device_owner ON console_sessions(device_id) WHERE state IN ('acquiring','active','protected','revoking','blocked');
CREATE UNIQUE INDEX console_server_request ON console_sessions(actor_id,server_id) WHERE state <> 'released';
CREATE TABLE console_tickets (
 token_hash text PRIMARY KEY, session_id text NOT NULL REFERENCES console_sessions(id), actor_id text NOT NULL REFERENCES users(id),
 generation bigint NOT NULL, expires_at timestamptz NOT NULL, consumed boolean NOT NULL DEFAULT false
);
CREATE TABLE console_access (
 token_hash text PRIMARY KEY, session_id text NOT NULL REFERENCES console_sessions(id), actor_id text NOT NULL REFERENCES users(id),
 generation bigint NOT NULL, expires_at timestamptz NOT NULL
);
CREATE TABLE media_assets (
 id text PRIMARY KEY, owner_id text NOT NULL REFERENCES users(id), session_id text NOT NULL REFERENCES console_sessions(id),
 filename text NOT NULL, size_bytes bigint NOT NULL CHECK(size_bytes>=0), sha256 text NOT NULL,
 state text NOT NULL CHECK(state IN ('ready','mounted','deleted')), created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE mock_console_state (
 device_id text PRIMARY KEY REFERENCES devices(id), selected_port integer, session_id text REFERENCES console_sessions(id),
 pressed_keys jsonb NOT NULL DEFAULT '[]', media_id text REFERENCES media_assets(id), frames bigint NOT NULL DEFAULT 0,
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE gateway_connections (
 id text PRIMARY KEY, session_id text NOT NULL REFERENCES console_sessions(id), generation bigint NOT NULL,
 opened_at timestamptz NOT NULL DEFAULT now()
);
