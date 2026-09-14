select *
from {{ source('search_qrels', 'qrel_history') }}
where available_at <= parseDateTime64BestEffort('{{ var("ingest_cutoff") }}', 6, 'UTC')
