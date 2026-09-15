CREATE SCHEMA IF NOT EXISTS warehouse_community;

CREATE TABLE IF NOT EXISTS warehouse_community.consumer_cursor (
  consumer text NOT NULL,
  producer text NOT NULL,
  committed_offset bigint NOT NULL DEFAULT 0 CHECK (committed_offset >= 0),
  PRIMARY KEY (consumer, producer)
);

CREATE TABLE IF NOT EXISTS warehouse_community.read_batch_evidence (
  consumer text NOT NULL,
  producer text NOT NULL,
  from_offset bigint NOT NULL CHECK (from_offset > 0),
  to_offset bigint NOT NULL CHECK (to_offset >= from_offset),
  batch_hash char(64) NOT NULL,
  committed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (consumer, producer, from_offset, to_offset, batch_hash)
);

CREATE INDEX IF NOT EXISTS read_batch_evidence_prefix
  ON warehouse_community.read_batch_evidence (consumer, producer, from_offset, to_offset);

CREATE TABLE IF NOT EXISTS warehouse_community.ods_event (
  producer text NOT NULL,
  source_offset bigint NOT NULL CHECK (source_offset > 0),
  event_id text NOT NULL,
  event_type text NOT NULL,
  aggregate_id text NOT NULL,
  aggregate_version bigint NOT NULL CHECK (aggregate_version > 0),
  operation_id text NOT NULL,
  occurred_at timestamptz NOT NULL,
  event_spec jsonb NOT NULL,
  source_event_hash char(64) NOT NULL,
  technical_receipt jsonb NOT NULL,
  receipt_id text NOT NULL,
  dc_received_at timestamptz NOT NULL,
  issuer text NOT NULL,
  subject_id text NOT NULL,
  target_type text NOT NULL,
  target_id text NOT NULL,
  target_revision text,
  revision_status text NOT NULL CHECK (revision_status = 'unknown'),
  operation text NOT NULL,
  source_ref text NOT NULL,
  comment_id text,
  parent_comment_id text,
  old_state smallint,
  new_state smallint,
  visibility_state smallint,
  search_evidence boolean,
  predecessor_event_id text,
  event_time timestamptz NOT NULL,
  available_at timestamptz NOT NULL,
  PRIMARY KEY (producer, source_offset),
  UNIQUE (producer, event_id),
  CHECK (issuer = 'rtw.identity'),
  CHECK (source_event_hash ~ '^[a-f0-9]{64}$'),
  CHECK (subject_id ~ '^[1-9][0-9]*$' AND length(subject_id) <= 19
         AND subject_id::numeric <= 9223372036854775807),
  CHECK ((producer = 'rtw.comment-rpc' AND event_type IN
         ('community.comment.created','community.comment.deleted','community.comment.interaction'))
      OR (producer = 'rtw.like-mq' AND event_type = 'community.target.interaction')),
  CHECK (target_revision IS NULL),
  CHECK (operation IN ('create','retract','like','unlike','dislike','undislike')),
  CHECK ((producer = 'rtw.comment-rpc' AND search_evidence = false AND comment_id IS NOT NULL)
      OR (producer = 'rtw.like-mq' AND search_evidence IS NULL AND comment_id IS NULL))
);

CREATE INDEX IF NOT EXISTS ods_event_subject_slice
  ON warehouse_community.ods_event (producer, issuer, subject_id, source_offset);

CREATE OR REPLACE FUNCTION warehouse_community.reject_immutable_source_change() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'community warehouse source evidence is immutable';
END;
$$;

DROP TRIGGER IF EXISTS community_ods_immutable ON warehouse_community.ods_event;
CREATE TRIGGER community_ods_immutable
BEFORE UPDATE OR DELETE ON warehouse_community.ods_event
FOR EACH ROW EXECUTE FUNCTION warehouse_community.reject_immutable_source_change();

DROP TRIGGER IF EXISTS community_batch_evidence_immutable ON warehouse_community.read_batch_evidence;
CREATE TRIGGER community_batch_evidence_immutable
BEFORE UPDATE OR DELETE ON warehouse_community.read_batch_evidence
FOR EACH ROW EXECUTE FUNCTION warehouse_community.reject_immutable_source_change();
