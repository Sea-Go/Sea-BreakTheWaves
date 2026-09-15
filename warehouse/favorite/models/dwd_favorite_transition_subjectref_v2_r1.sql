{{ config(enabled=var('favorite_subjectref_v2_read', false)) }}
-- Each original (producer,offset,event_id) is one logical transition even
-- when its v1 ODS row and v2 sidecar row are both present.
with versioned as (
  select *, authority_id as issuer, subject_id as subject_uid,
    1 as representation_version
  from {{ source('favorite_landing', 'ods_favorite_event') }}
  union all
  select producer,source_offset,event_id,event_type,authority_id,tenant_id,
    subject_id,favorite_id,folder_id,target_type,target_id,target_revision,
    operation,predecessor_event_id,event_time,available_at,dc_received_at,
    source_event_hash,technical_receipt,event_spec,issuer,subject_uid,
    2 as representation_version
  from {{ source('favorite_landing', 'ods_favorite_event_subjectref_v2_r1') }}
), chosen as (
  select *, row_number() over (
    partition by producer,source_offset,event_id
    order by representation_version desc
  ) as representation_rank
  from versioned
)
select
  producer,source_offset,event_id,event_type,issuer,subject_uid,
  authority_id as original_authority_id,
  tenant_id as original_tenant_id,
  subject_id as original_subject_id,
  favorite_id,folder_id,target_type,target_id,target_revision,operation,
  predecessor_event_id,
  if(operation='assert', 1, -1) as favorite_state_delta,
  if(operation='assert', 1, 0) as active_after,
  event_time,available_at,dc_received_at,source_event_hash,
  representation_version, 'favorite.transition.subjectref.v2.r1' as row_contract
from chosen where representation_rank=1
