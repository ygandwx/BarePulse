CREATE TABLE integrations (
 id text PRIMARY KEY, name text UNIQUE NOT NULL, secret_ref text NOT NULL, enabled boolean NOT NULL DEFAULT true,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE service_mappings (
 id text PRIMARY KEY, integration_id text NOT NULL REFERENCES integrations(id), service_id text NOT NULL,
 client_id text NOT NULL, owner_id text NOT NULL REFERENCES users(id), server_id text NOT NULL REFERENCES servers(id),
 state text NOT NULL DEFAULT 'active' CHECK(state IN ('active','suspended','terminated')), version bigint NOT NULL DEFAULT 1,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(), UNIQUE(integration_id,service_id)
);
CREATE UNIQUE INDEX mapped_server ON service_mappings(server_id) WHERE state<>'terminated';
CREATE TABLE integration_nonces (
 integration_id text NOT NULL REFERENCES integrations(id), nonce text NOT NULL, expires_at timestamptz NOT NULL,
 PRIMARY KEY(integration_id,nonce)
);
CREATE TABLE portal_grants (
 token_hash text PRIMARY KEY, mapping_id text NOT NULL REFERENCES service_mappings(id), session_id text NOT NULL REFERENCES console_sessions(id),
 actor_id text NOT NULL REFERENCES users(id), consumed boolean NOT NULL DEFAULT false, kind text NOT NULL CHECK(kind IN ('ticket','access')),
 expires_at timestamptz NOT NULL
);
