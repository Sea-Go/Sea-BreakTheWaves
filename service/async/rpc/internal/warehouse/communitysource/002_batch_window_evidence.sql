-- Earlier isolated builds keyed a read window only by from_offset. A lost ACK
-- can legitimately produce a shorter/longer next read at the same from_offset.
-- Preserve every existing row while widening the identity to the exact DC
-- window and hash; old rows remain immutable provenance.
DO $$
DECLARE current_name text;
DECLARE current_definition text;
BEGIN
  SELECT conname, pg_get_constraintdef(oid) INTO current_name, current_definition
  FROM pg_constraint
  WHERE conrelid = 'warehouse_community.read_batch_evidence'::regclass
    AND contype = 'p';
  IF current_name IS NULL THEN
    ALTER TABLE warehouse_community.read_batch_evidence
      ADD CONSTRAINT read_batch_evidence_pkey
      PRIMARY KEY (consumer, producer, from_offset, to_offset, batch_hash);
  ELSIF current_definition <> 'PRIMARY KEY (consumer, producer, from_offset, to_offset, batch_hash)' THEN
    EXECUTE format('ALTER TABLE warehouse_community.read_batch_evidence DROP CONSTRAINT %I', current_name);
    ALTER TABLE warehouse_community.read_batch_evidence
      ADD CONSTRAINT read_batch_evidence_pkey
      PRIMARY KEY (consumer, producer, from_offset, to_offset, batch_hash);
  END IF;
END;
$$;
