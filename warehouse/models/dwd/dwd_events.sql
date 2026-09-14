-- Input conflicts are rejected by tests before export; exact redelivery is one fact.
select * except (delivery_number)
from (
  select *, row_number() over (
    partition by event_id order by available_at, batch_id, payload_hash
  ) as delivery_number
  from {{ ref('ods_events') }}
)
where delivery_number = 1
