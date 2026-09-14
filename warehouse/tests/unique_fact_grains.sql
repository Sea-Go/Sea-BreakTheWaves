select 'request' as grain, request_id as id from {{ ref('dwd_requests') }}
group by request_id having count() > 1
union all
select 'impression', impression_id from {{ ref('dwd_impressions') }}
group by impression_id having count() > 1
union all
select 'snapshot', feature_snapshot_ref from {{ ref('dwd_feature_snapshots') }}
group by feature_snapshot_ref having count() > 1
union all
select 'content_revision', concat(item_id, ':', content_revision) from {{ ref('dim_content_history') }}
group by item_id, content_revision having count() > 1
union all
select 'candidate', concat(request_id, ':', candidate_id, ':', stage, ':', stage_invocation_id, ':', toString(attempt))
from {{ ref('dwd_candidates') }}
group by request_id, candidate_id, stage, stage_invocation_id, attempt having count() > 1
