CREATE TABLE traffic_accounts (
 id text PRIMARY KEY, name text NOT NULL, owner_id text NOT NULL REFERENCES users(id),
 quota_bytes numeric(30,0) NOT NULL CHECK(quota_bytes>=0),
 direction text NOT NULL CHECK(direction IN ('upload','download','both')),
 cycle_day integer NOT NULL DEFAULT 1 CHECK(cycle_day BETWEEN 1 AND 31),
 timezone text NOT NULL DEFAULT 'UTC', alert_percent integer NOT NULL DEFAULT 80 CHECK(alert_percent BETWEEN 0 AND 100),
 enforcement_enabled boolean NOT NULL DEFAULT false CHECK(NOT enforcement_enabled),
 created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(owner_id,name)
);
CREATE TABLE traffic_members (
 account_id text NOT NULL REFERENCES traffic_accounts(id), server_id text NOT NULL REFERENCES servers(id),
 server_version bigint NOT NULL, active boolean NOT NULL DEFAULT true, created_at timestamptz NOT NULL DEFAULT now(), ended_at timestamptz,
 PRIMARY KEY(account_id,server_id), CHECK(active = (ended_at IS NULL))
);
CREATE UNIQUE INDEX traffic_member_active ON traffic_members(server_id) WHERE active;
CREATE TABLE traffic_sources (
 id text PRIMARY KEY, account_id text NOT NULL REFERENCES traffic_accounts(id), device_id text NOT NULL REFERENCES devices(id),
 binding_id text REFERENCES bindings(id), binding_version bigint NOT NULL DEFAULT 0,
 kind text NOT NULL CHECK(kind IN ('port','vlan')), role text NOT NULL CHECK(role IN ('primary','observation')),
 network_domain text NOT NULL, vlan_id integer NOT NULL CHECK(vlan_id BETWEEN 1 AND 4094),
 if_name text NOT NULL, expected_if_index integer NOT NULL CHECK(expected_if_index>0),
 direction text NOT NULL DEFAULT 'rx_upload' CHECK(direction IN ('rx_upload','rx_download')),
 interval_seconds integer NOT NULL DEFAULT 60 CHECK(interval_seconds BETWEEN 10 AND 3600),
 max_bps numeric(30,0) NOT NULL DEFAULT 0 CHECK(max_bps>=0),
 mock boolean NOT NULL DEFAULT true, auto_collect boolean NOT NULL DEFAULT false,
 active boolean NOT NULL DEFAULT true, created_at timestamptz NOT NULL DEFAULT now(), ended_at timestamptz,
 CHECK((kind='port' AND binding_id IS NOT NULL) OR (kind='vlan' AND binding_id IS NULL)),
 CHECK(active = (ended_at IS NULL))
);
CREATE UNIQUE INDEX traffic_primary_interface ON traffic_sources(device_id,if_name) WHERE active AND role='primary';
CREATE UNIQUE INDEX traffic_primary_ifindex ON traffic_sources(device_id,expected_if_index) WHERE active AND role='primary';
CREATE UNIQUE INDEX traffic_primary_binding ON traffic_sources(binding_id) WHERE active AND role='primary' AND binding_id IS NOT NULL;
CREATE INDEX traffic_source_account ON traffic_sources(account_id);
-- This transaction-wide topology lock also serializes concurrent inserts on an empty domain.
-- Port/VLAN overlap is rejected across accounts as well as inside one account.
CREATE FUNCTION traffic_source_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 PERFORM pg_advisory_xact_lock(834920114);
 IF TG_OP='UPDATE' AND (NEW.account_id,NEW.device_id,NEW.binding_id,NEW.binding_version,NEW.kind,NEW.role,NEW.network_domain,NEW.vlan_id,NEW.if_name,NEW.expected_if_index,NEW.direction,NEW.mock)
   IS DISTINCT FROM (OLD.account_id,OLD.device_id,OLD.binding_id,OLD.binding_version,OLD.kind,OLD.role,OLD.network_domain,OLD.vlan_id,OLD.if_name,OLD.expected_if_index,OLD.direction,OLD.mock) THEN
  RAISE EXCEPTION 'traffic source identity is immutable; retire and replace it' USING ERRCODE='23514';
 END IF;
 IF NEW.active AND NEW.role='primary' AND EXISTS (
  SELECT 1 FROM traffic_sources s WHERE s.id<>NEW.id AND s.active AND s.role='primary'
    AND s.network_domain=NEW.network_domain AND s.vlan_id=NEW.vlan_id AND (s.kind='vlan' OR NEW.kind='vlan')
 ) THEN
  RAISE EXCEPTION 'port/VLAN accounting overlap' USING ERRCODE='23505';
 END IF;
 IF NEW.active AND EXISTS (
  SELECT 1 FROM traffic_sources s WHERE s.id<>NEW.id AND s.active AND s.account_id<>NEW.account_id
    AND s.network_domain=NEW.network_domain AND s.vlan_id=NEW.vlan_id
    AND ((s.kind='vlan' AND s.role='primary' AND NEW.kind='port') OR (NEW.kind='vlan' AND NEW.role='primary' AND s.kind='port'))
 ) THEN
  RAISE EXCEPTION 'VLAN contains another account member' USING ERRCODE='23505';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER traffic_source_guard BEFORE INSERT OR UPDATE ON traffic_sources FOR EACH ROW EXECUTE FUNCTION traffic_source_guard();
CREATE TABLE traffic_samples (
 id text PRIMARY KEY, source_id text NOT NULL REFERENCES traffic_sources(id), sample_key text NOT NULL,
 observed_at timestamptz NOT NULL, in_octets numeric(20,0), out_octets numeric(20,0),
 if_index integer NOT NULL, if_name text NOT NULL, if_alias text NOT NULL DEFAULT '',
 uptime numeric(20,0) NOT NULL, discontinuity numeric(20,0) NOT NULL, link_state text NOT NULL,
 binding_version bigint NOT NULL, quality text NOT NULL, error_code text NOT NULL DEFAULT '',
 payload_hash text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(source_id,sample_key), UNIQUE(source_id,observed_at),
 CHECK(in_octets IS NULL OR in_octets BETWEEN 0 AND 18446744073709551615),
 CHECK(out_octets IS NULL OR out_octets BETWEEN 0 AND 18446744073709551615)
);
CREATE INDEX traffic_sample_latest ON traffic_samples(source_id,observed_at DESC);
CREATE TABLE traffic_ledger (
 id text PRIMARY KEY, account_id text NOT NULL REFERENCES traffic_accounts(id), source_id text NOT NULL REFERENCES traffic_sources(id),
 start_sample_id text NOT NULL REFERENCES traffic_samples(id), end_sample_id text NOT NULL REFERENCES traffic_samples(id),
 starts_at timestamptz NOT NULL, ends_at timestamptz NOT NULL, period_start timestamptz,
 upload_bytes numeric(30,0), download_bytes numeric(30,0), quality text NOT NULL,
 billable boolean NOT NULL DEFAULT false, created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(source_id,end_sample_id), CHECK(ends_at>starts_at),
 CHECK(upload_bytes IS NULL OR upload_bytes>=0), CHECK(download_bytes IS NULL OR download_bytes>=0)
);
CREATE INDEX traffic_ledger_period ON traffic_ledger(account_id,period_start);
CREATE TABLE traffic_adjustments (
 id text PRIMARY KEY, account_id text NOT NULL REFERENCES traffic_accounts(id), actor_id text NOT NULL REFERENCES users(id),
 period_start timestamptz NOT NULL, bytes numeric(30,0) NOT NULL, reason text NOT NULL,
 idempotency_key text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(account_id,idempotency_key)
);
CREATE TABLE traffic_alerts (
 id text PRIMARY KEY, account_id text NOT NULL REFERENCES traffic_accounts(id), period_start timestamptz NOT NULL,
 kind text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(account_id,period_start,kind)
);
