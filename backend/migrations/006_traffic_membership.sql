-- Preserve each membership interval when a server leaves and later rejoins.
ALTER TABLE traffic_members DROP CONSTRAINT traffic_members_pkey;
ALTER TABLE traffic_members ADD COLUMN id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY;

-- VLAN coverage belongs to the source, not to one billing account's membership.
-- Observations may cover several accounts, but only one customer.
CREATE TABLE traffic_source_members (
 source_id text NOT NULL REFERENCES traffic_sources(id),
 server_id text NOT NULL REFERENCES servers(id),
 server_version bigint NOT NULL,
 PRIMARY KEY(source_id,server_id)
);
INSERT INTO traffic_source_members(source_id,server_id,server_version)
 SELECT s.id,m.server_id,m.server_version FROM traffic_sources s JOIN traffic_members m ON m.account_id=s.account_id
 WHERE s.kind='vlan' AND s.active AND m.active;
CREATE INDEX traffic_source_member_server ON traffic_source_members(server_id);

-- Creating an additional port cannot silently expand an existing observation's
-- explicitly confirmed coverage, including into a different customer's VLAN.
CREATE FUNCTION traffic_observation_port_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.active AND NEW.kind='port' AND EXISTS (
  SELECT 1 FROM traffic_sources v JOIN traffic_accounts va ON va.id=v.account_id
  JOIN bindings b ON b.id=NEW.binding_id JOIN servers srv ON srv.id=b.server_id
  WHERE v.active AND v.kind='vlan' AND v.role='observation'
   AND v.network_domain=NEW.network_domain AND v.vlan_id=NEW.vlan_id
   AND (va.owner_id<>srv.owner_id OR NOT EXISTS (
    SELECT 1 FROM traffic_source_members m WHERE m.source_id=v.id AND m.server_id=srv.id AND m.server_version=srv.version
   ))
 ) THEN
  RAISE EXCEPTION 'port is outside confirmed VLAN observation coverage; retire observation first' USING ERRCODE='23505';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER traffic_z_observation_port_guard BEFORE INSERT OR UPDATE ON traffic_sources
 FOR EACH ROW EXECUTE FUNCTION traffic_observation_port_guard();
