select query_family_id as leaked_group
from {{ ref('ds_search_qrels') }}
group by query_family_id
having uniqExact(split) > 1
union all
select near_duplicate_cluster_id as leaked_group
from {{ ref('ds_search_qrels') }}
group by near_duplicate_cluster_id
having uniqExact(split) > 1
