select i.impression_id as impression_id, i.request_id as request_id,
       i.authority_id as authority_id, i.tenant_id as tenant_id, i.subject_id as subject_id,
       i.item_id as item_id, i.content_revision as content_revision,
       i.event_time as impression_time, r.request_time as request_time,
       r.feature_cutoff as feature_cutoff,
       r.feature_snapshot_ref as feature_snapshot_ref, r.feature_contract_id as feature_contract_id,
       f.user_interest as user_interest, f.item_quality as item_quality,
       f.available_at as feature_available_at,
       multiIf(
         r.event_id is null, 'missing_request',
         c.event_id is null, 'missing_served_candidate',
         f.event_id is null, 'missing_feature_snapshot',
         d.event_id is null, 'missing_content_revision',
         tuple(i.authority_id, i.tenant_id, i.subject_id) != tuple(r.authority_id, r.tenant_id, r.subject_id) or tuple(i.authority_id, i.tenant_id, i.subject_id) != tuple(f.authority_id, f.tenant_id, f.subject_id),
           'subject_mismatch',
         f.item_id != i.item_id or f.content_revision != i.content_revision,
           'feature_item_mismatch',
         f.available_at > r.feature_cutoff or f.effective_at > r.feature_cutoff,
           'future_feature',
         d.available_at > r.feature_cutoff or d.effective_at > r.feature_cutoff,
           'future_content',
         r.feature_cutoff < r.request_time or i.event_time < r.feature_cutoff, 'invalid_feature_cutoff',
         r.feature_contract_id != {{ literal(var('feature_contract_id')) }},
           'feature_contract_mismatch',
         'eligible'
       ) as qualification
from {{ ref('dwd_impressions') }} i
left join {{ ref('dwd_requests') }} r on i.request_id = r.request_id
left join {{ ref('dwd_candidates') }} c
  on i.request_id = c.request_id and i.candidate_id = c.candidate_id
 and i.item_id = c.item_id and i.content_revision = c.content_revision
 and i.stage_invocation_id = c.stage_invocation_id and i.attempt = c.attempt
 and c.stage = 'served'
left join {{ ref('dwd_feature_snapshots') }} f
  on r.feature_snapshot_ref = f.feature_snapshot_ref
 and r.feature_contract_id = f.feature_contract_id
left join {{ ref('dim_content_history') }} d
  on i.item_id = d.item_id and i.content_revision = d.content_revision
