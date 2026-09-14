select event_id, impression_id, request_id, candidate_id, authority_id, tenant_id, subject_id,
       item_id, content_revision, stage_invocation_id, attempt,
       event_time, available_at
from {{ ref('dwd_events') }}
where event_type = 'impression' and domain = 'recommendation'
  and JSONExtractBool(payload, 'visible')
