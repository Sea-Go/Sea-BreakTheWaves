select event_id, feature_snapshot_ref, feature_contract_id, authority_id, tenant_id, subject_id,
       item_id, content_revision, event_time as effective_at, available_at,
       JSONExtractFloat(payload, 'user_interest') as user_interest,
       JSONExtractFloat(payload, 'item_quality') as item_quality
from {{ ref('dwd_events') }}
where event_type = 'feature_snapshot' and domain = 'recommendation'
