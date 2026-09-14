select judgment_id
from {{ ref('ds_search_qrels') }}
where judged_mask = false or relevance_grade > 3 or split = 'out_of_range'
   or content_available_at > query_time or query_time > judged_at
   or judged_at > available_at
   or available_at > parseDateTime64BestEffort('{{ var("ingest_cutoff") }}', 6, 'UTC')
   or (revoked_at is not null and revoked_at <= parseDateTime64BestEffort('{{ var("ingest_cutoff") }}', 6, 'UTC'))
   or query_text = '' or chunk_text = '' or judgment_source_ref = ''
