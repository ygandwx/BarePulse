-- A physical PiKVM port cannot be split between two servers across power/console roles.
CREATE FUNCTION enforce_physical_binding() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.active AND NEW.kind IN ('power','console') THEN
  PERFORM 1 FROM devices WHERE id=NEW.device_id FOR UPDATE;
  IF EXISTS(SELECT 1 FROM bindings b WHERE b.active AND b.device_id=NEW.device_id AND b.port=NEW.port
     AND b.kind IN ('power','console') AND b.server_id<>NEW.server_id AND b.id<>NEW.id) THEN
   RAISE EXCEPTION 'physical port already bound to another server' USING ERRCODE='23505';
  END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER physical_binding_guard BEFORE INSERT OR UPDATE ON bindings FOR EACH ROW EXECUTE FUNCTION enforce_physical_binding();
