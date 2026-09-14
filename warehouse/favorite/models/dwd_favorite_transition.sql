-- One row per RTW favorite source version. A retract is a state transition,
-- never a negative recommendation label or an impression denominator.
select
  producer,
  source_offset,
  event_id,
  event_type,
  authority_id,
  tenant_id,
  subject_id,
  favorite_id,
  folder_id,
  target_type,
  target_id,
  target_revision,
  operation,
  predecessor_event_id,
  if(operation = 'assert', 1, -1) as favorite_state_delta,
  if(operation = 'assert', 1, 0) as active_after,
  event_time,
  available_at,
  dc_received_at,
  source_event_hash
from {{ source('favorite_landing', 'ods_favorite_event') }}
