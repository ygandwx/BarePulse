CREATE TABLE users (
 id text PRIMARY KEY, email text UNIQUE NOT NULL, password_hash text NOT NULL,
 role text NOT NULL CHECK(role IN ('admin','operator','customer')), created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE auth_sessions (
 token_hash text PRIMARY KEY, user_id text NOT NULL REFERENCES users(id), csrf text NOT NULL,
 expires_at timestamptz NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE servers (
 id text PRIMARY KEY, name text NOT NULL UNIQUE, owner_id text NOT NULL REFERENCES users(id),
 model text NOT NULL DEFAULT '', notes text NOT NULL DEFAULT '', maintenance boolean NOT NULL DEFAULT false,
 version bigint NOT NULL DEFAULT 1, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE devices (
 id text PRIMARY KEY, name text UNIQUE NOT NULL,
 kind text NOT NULL CHECK(kind IN ('mock_pikvm','mock_pdu','mock_bmc','pikvm','apc','redfish','ipmi','snmp')),
 address text NOT NULL DEFAULT '', credential_ref text NOT NULL DEFAULT '',
 scenario text NOT NULL DEFAULT 'success' CHECK(scenario IN ('success','offline','inner_error','timeout','busy')),
 enabled boolean NOT NULL DEFAULT true, version bigint NOT NULL DEFAULT 1,
 lease_owner text, lease_kind text, blocked_reason text NOT NULL DEFAULT '', cooldown_until timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE bindings (
 id text PRIMARY KEY, server_id text NOT NULL REFERENCES servers(id), device_id text NOT NULL REFERENCES devices(id),
 kind text NOT NULL CHECK(kind IN ('power','console','network')), port integer NOT NULL CHECK(port>0),
 version bigint NOT NULL DEFAULT 1, active boolean NOT NULL DEFAULT true,
 created_at timestamptz NOT NULL DEFAULT now(), ended_at timestamptz,
 CHECK(active = (ended_at IS NULL))
);
CREATE UNIQUE INDEX binding_device_port ON bindings(device_id,kind,port) WHERE active;
CREATE UNIQUE INDEX binding_server_kind ON bindings(server_id,kind) WHERE active AND kind IN ('power','console');
CREATE TABLE operation_tasks (
 id text PRIMARY KEY, actor_id text NOT NULL REFERENCES users(id), server_id text NOT NULL REFERENCES servers(id),
 device_id text NOT NULL REFERENCES devices(id), binding_id text NOT NULL REFERENCES bindings(id),
 binding_version bigint NOT NULL, server_version bigint NOT NULL, device_version bigint NOT NULL, action text NOT NULL, idempotency_key text NOT NULL,
 status text NOT NULL CHECK(status IN ('queued','running','verifying','succeeded','failed','unknown','cancelled')),
 result jsonb NOT NULL DEFAULT '{}', created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(actor_id,server_id,idempotency_key)
);
CREATE INDEX task_queue ON operation_tasks(created_at) WHERE status='queued';
CREATE TABLE audit_events (
 id text PRIMARY KEY, actor_id text REFERENCES users(id), action text NOT NULL, object_id text NOT NULL,
 details jsonb NOT NULL DEFAULT '{}', created_at timestamptz NOT NULL DEFAULT now()
);
