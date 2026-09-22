CREATE TABLE IF NOT EXISTS warehouse_favorite.coverage_batch_evidence (
  consumer text NOT NULL,
  producer text NOT NULL,
  from_offset bigint NOT NULL CHECK (from_offset > 0),
  to_offset bigint NOT NULL CHECK (to_offset >= from_offset),
  batch_hash char(64) NOT NULL,
  PRIMARY KEY (consumer,producer,from_offset)
);

CREATE TABLE IF NOT EXISTS warehouse_favorite.coverage_publication (
  manifest_sha256 char(64) PRIMARY KEY,
  generation text NOT NULL UNIQUE,
  producer text NOT NULL,
  through_offset bigint NOT NULL CHECK (through_offset > 0),
  event_index_sha256 char(64) NOT NULL,
  batch_evidence_sha256 char(64) NOT NULL
);

CREATE TABLE IF NOT EXISTS warehouse_favorite.coverage_subject_receipt (
  receipt_sha256 char(64) PRIMARY KEY,
  manifest_sha256 char(64) NOT NULL REFERENCES warehouse_favorite.coverage_publication(manifest_sha256),
  authority_id text NOT NULL,
  tenant_id text NOT NULL,
  subject_id text NOT NULL,
  sparse_index_sha256 char(64) NOT NULL,
  event_count bigint NOT NULL CHECK (event_count >= 0),
  UNIQUE (manifest_sha256,authority_id,tenant_id,subject_id)
);
