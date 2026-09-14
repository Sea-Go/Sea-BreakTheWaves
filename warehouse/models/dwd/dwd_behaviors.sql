select event_id, impression_id, request_id, authority_id, tenant_id, subject_id, item_id,
       content_revision, event_time, available_at,
       JSONExtractString(payload, 'action') as action
from {{ ref('dwd_events') }}
where event_type = 'behavior' and domain = 'recommendation'
