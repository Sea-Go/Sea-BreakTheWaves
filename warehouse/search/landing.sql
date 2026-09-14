CREATE TABLE IF NOT EXISTS {database}.qrel_history (
  batch_id String, event_id String, judgment_id String, judgment_revision UInt32,
  status LowCardinality(String), query_id String, query_family_id String,
  near_duplicate_cluster_id String, query_text String, query_text_sha256 String,
  document_id String, document_revision String, chunk_id String, chunk_text String,
  chunk_text_sha256 String, relevance_grade Nullable(UInt8), judged_mask Bool,
  judgment_source String, judgment_source_ref String, judgment_source_hash String,
  query_time DateTime64(6, 'UTC'), content_available_at DateTime64(6, 'UTC'),
  judged_at DateTime64(6, 'UTC'), available_at DateTime64(6, 'UTC'),
  revoked_at Nullable(DateTime64(6, 'UTC')),
  source_partition String, source_sequence UInt64, payload_hash String
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(available_at)
ORDER BY (batch_id, judgment_id, judgment_revision, event_id, source_sequence)
