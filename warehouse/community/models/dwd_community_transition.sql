-- One row per admitted source transition. This relation contains no display,
-- impression, unclicked-negative, content-revision, label, sample, or feature semantics.
select
  producer,
  source_offset,
  event_id,
  event_type,
  aggregate_id,
  aggregate_version as source_version,
  operation_id,
  issuer,
  subject_id,
  target_type,
  target_id,
  target_revision,
  revision_status,
  operation,
  multiIf(
    event_type = 'community.comment.created', 'comment_create',
    event_type = 'community.comment.deleted', 'comment_delete',
    producer = 'rtw.comment-rpc', concat('comment_', operation),
    concat('target_', operation)
  ) as transition_type,
  source_ref,
  comment_id,
  parent_comment_id,
  old_state,
  new_state,
  visibility_state,
  search_evidence,
  predecessor_event_id,
  event_time,
  available_at,
  dc_received_at,
  source_event_hash
from {{ source('community_landing', 'ods_community_event') }}
where
  (producer = 'rtw.comment-rpc' and event_type in
    ('community.comment.created', 'community.comment.deleted', 'community.comment.interaction'))
  or
  (producer = 'rtw.like-mq' and event_type = 'community.target.interaction' and operation in ('like', 'unlike'))
