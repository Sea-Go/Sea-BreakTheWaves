select event_id from {{ ref('ods_events') }}
group by event_id having uniqExact(payload_hash) > 1
