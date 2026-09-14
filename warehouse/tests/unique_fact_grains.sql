select 'request' as grain, toJSONString(tuple(authority_id, tenant_id, subject_id, request_id)) as id
from {{ ref('dwd_requests') }}
group by authority_id, tenant_id, subject_id, request_id having count() > 1
union all
select 'impression', toJSONString(tuple(authority_id, tenant_id, subject_id, impression_id))
from {{ ref('dwd_impressions') }}
group by authority_id, tenant_id, subject_id, impression_id having count() > 1
union all
select 'snapshot', toJSONString(tuple(authority_id, tenant_id, subject_id, feature_contract_id, feature_snapshot_ref))
from {{ ref('dwd_feature_snapshots') }}
group by authority_id, tenant_id, subject_id, feature_contract_id, feature_snapshot_ref having count() > 1
union all
select 'content_revision', toJSONString(tuple(item_id, content_revision)) from {{ ref('dim_content_history') }}
group by item_id, content_revision having count() > 1
union all
select 'candidate', toJSONString(tuple(authority_id, tenant_id, subject_id, request_id, candidate_id, stage, stage_invocation_id, attempt))
from {{ ref('dwd_candidates') }}
group by authority_id, tenant_id, subject_id, request_id, candidate_id, stage, stage_invocation_id, attempt having count() > 1
