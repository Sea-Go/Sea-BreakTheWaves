CREATE SCHEMA IF NOT EXISTS warehouse_community;

CREATE TABLE IF NOT EXISTS warehouse_community.coverage_prefix (
  producer text NOT NULL,
  generation text NOT NULL,
  through_offset bigint NOT NULL CHECK (through_offset > 0),
  event_index_sha256 char(64) NOT NULL,
  batch_evidence_sha256 char(64) NOT NULL,
  manifest_sha256 char(64) NOT NULL,
  manifest_url text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (producer, generation),
  UNIQUE (producer, manifest_sha256),
  CHECK (event_index_sha256 ~ '^[a-f0-9]{64}$'),
  CHECK (batch_evidence_sha256 ~ '^[a-f0-9]{64}$'),
  CHECK (manifest_sha256 ~ '^[a-f0-9]{64}$')
);

CREATE TABLE IF NOT EXISTS warehouse_community.coverage_subject (
  producer text NOT NULL,
  prefix_manifest_sha256 char(64) NOT NULL,
  issuer text NOT NULL CHECK (issuer = 'rtw.identity'),
  subject_id text NOT NULL CHECK (subject_id ~ '^[1-9][0-9]*$' AND length(subject_id) <= 19
                                  AND subject_id::numeric <= 9223372036854775807),
  event_count bigint NOT NULL CHECK (event_count >= 0),
  sparse_index_sha256 char(64) NOT NULL,
  receipt_sha256 char(64) NOT NULL,
  receipt_url text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (producer, prefix_manifest_sha256, issuer, subject_id),
  CHECK (prefix_manifest_sha256 ~ '^[a-f0-9]{64}$'),
  CHECK (sparse_index_sha256 ~ '^[a-f0-9]{64}$'),
  CHECK (receipt_sha256 ~ '^[a-f0-9]{64}$')
);
