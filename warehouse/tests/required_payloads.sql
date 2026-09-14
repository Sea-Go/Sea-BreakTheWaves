select event_id from {{ ref('dwd_events') }}
where empty(event_id) or empty(authority_id) or empty(tenant_id) or empty(subject_id)
   or (event_type = 'feature_snapshot' and (
       not JSONHas(payload, 'user_interest') or not JSONHas(payload, 'item_quality')
       or JSONType(payload, 'user_interest') not in ('Double', 'Int64', 'UInt64')
       or JSONType(payload, 'item_quality') not in ('Double', 'Int64', 'UInt64')
       or empty(feature_snapshot_ref) or empty(feature_contract_id)))
   or (event_type = 'impression' and (empty(impression_id) or empty(request_id)))
   or (event_type = 'request' and not JSONHas(payload, 'feature_cutoff'))
