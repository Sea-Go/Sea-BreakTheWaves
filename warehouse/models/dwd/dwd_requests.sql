select event_id, request_id, authority_id, tenant_id, subject_id, event_time as request_time,
       feature_snapshot_ref, feature_contract_id,
       parseDateTime64BestEffort(JSONExtractString(payload, 'feature_cutoff'), 6, 'UTC') as feature_cutoff
from {{ ref('dwd_events') }}
where event_type = 'request' and domain = 'recommendation'
