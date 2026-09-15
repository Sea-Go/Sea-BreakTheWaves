-- A new, append-only CH representation of the already admitted v1 ODS row.
-- This revision is deliberately limited to a PG-verified historic-v1 sidecar;
-- a future v2 wire event needs its own row contract and source verification.
CREATE TABLE IF NOT EXISTS {database}.ods_favorite_event_subjectref_v2_r1 (
  producer String,
  source_offset Int64,
  event_id String,
  event_type String,
  authority_id String,
  tenant_id String,
  subject_id String,
  issuer String,
  subject_uid String,
  favorite_id String,
  folder_id String,
  target_type String,
  target_id String,
  target_revision Nullable(String),
  operation String,
  predecessor_event_id String,
  event_time DateTime64(9, 'UTC'),
  available_at DateTime64(9, 'UTC'),
  dc_received_at DateTime64(9, 'UTC'),
  source_event_hash String,
  technical_receipt String,
  event_spec String,
  origin_ods_sha256 String
) ENGINE = MergeTree ORDER BY (producer, source_offset, event_id);
