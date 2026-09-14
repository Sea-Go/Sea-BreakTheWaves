-- The previous dispatch row used one epoch for two unrelated authorities.
-- Backfill old rows without changing their immutable READY/outbox state; new
-- workers must supply the separate current DC and RTW leases explicitly.
ALTER TABLE content_index_dispatch ADD COLUMN IF NOT EXISTS dc_attempt_id text;
ALTER TABLE content_index_dispatch ADD COLUMN IF NOT EXISTS dc_lease_epoch bigint;
ALTER TABLE content_index_dispatch ADD COLUMN IF NOT EXISTS dc_cancel_version bigint;
ALTER TABLE content_index_dispatch ADD COLUMN IF NOT EXISTS dc_lease_expires_at timestamptz;
UPDATE content_index_dispatch SET
    dc_attempt_id=COALESCE(dc_attempt_id,attempt_id),
    dc_lease_epoch=COALESCE(dc_lease_epoch,lease_epoch),
    dc_cancel_version=COALESCE(dc_cancel_version,cancel_version),
    dc_lease_expires_at=COALESCE(dc_lease_expires_at,lease_expires_at)
WHERE dc_attempt_id IS NULL OR dc_lease_epoch IS NULL OR dc_cancel_version IS NULL OR dc_lease_expires_at IS NULL;
ALTER TABLE content_index_dispatch ALTER COLUMN dc_attempt_id SET NOT NULL;
ALTER TABLE content_index_dispatch ALTER COLUMN dc_lease_epoch SET NOT NULL;
ALTER TABLE content_index_dispatch ALTER COLUMN dc_cancel_version SET NOT NULL;
ALTER TABLE content_index_dispatch ALTER COLUMN dc_lease_expires_at SET NOT NULL;
DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='content_index_dispatch_dc_fence_valid'
        AND conrelid='content_index_dispatch'::regclass) THEN
        ALTER TABLE content_index_dispatch ADD CONSTRAINT content_index_dispatch_dc_fence_valid
            CHECK (dc_attempt_id<>'' AND dc_lease_epoch>0 AND dc_cancel_version>=0);
    END IF;
END $$;
