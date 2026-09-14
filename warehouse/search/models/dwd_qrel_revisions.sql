-- Exact redelivery is one event. Conflicting redelivery is rejected by tests.
select * except (_delivery_rank),
       row_number() over (
         partition by judgment_id
         order by judgment_revision desc, available_at desc, source_sequence desc
       ) as _revision_rank
from (
  select *, row_number() over (
    partition by event_id order by source_sequence, batch_id, payload_hash
  ) as _delivery_rank
  from {{ ref('ods_qrel_events') }}
)
where _delivery_rank = 1
