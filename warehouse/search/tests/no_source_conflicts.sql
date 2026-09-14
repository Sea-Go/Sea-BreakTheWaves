select event_id
from {{ ref('ods_qrel_events') }}
group by event_id
having uniqExact(payload_hash) != 1
union all
select judgment_id
from {{ ref('ods_qrel_events') }}
group by judgment_id, judgment_revision
having uniqExact(payload_hash) != 1
