select domain, authority_id, tenant_id, subject_id, event_id from {{ ref('ods_events') }}
group by domain, authority_id, tenant_id, subject_id, event_id having uniqExact(payload_hash) > 1
