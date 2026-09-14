CREATE TABLE IF NOT EXISTS {database}.event_history (
  batch_id String, event_id String, domain LowCardinality(String),
  event_type LowCardinality(String), authority_id String, tenant_id String, subject_id String,
  request_id String, candidate_id String, impression_id String, item_id String,
  content_revision String, stage String, stage_invocation_id String, attempt UInt32,
  feature_snapshot_ref String, feature_contract_id String,
  event_time DateTime64(6, 'UTC'), available_at DateTime64(6, 'UTC'),
  source_partition String, source_sequence UInt64, payload String, payload_hash String
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(available_at)
ORDER BY (batch_id, event_type, event_id, source_sequence)
