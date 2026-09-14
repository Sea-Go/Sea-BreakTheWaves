-- Immutable revision key. A newer content revision never erases this row.
select event_id, item_id, content_revision, event_time as effective_at,
       available_at, JSONExtractString(payload, 'title') as title
from {{ ref('dwd_events') }}
where event_type = 'content_revision'
