select event_id, request_id, candidate_id, item_id, content_revision,
       stage, stage_invocation_id, attempt, event_time, available_at
from {{ ref('dwd_events') }}
where event_type = 'candidate' and domain = 'recommendation'
