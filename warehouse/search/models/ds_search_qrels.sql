-- Source revisions are visible only at the frozen ingest cutoff. The row is
-- the judged query/document/chunk, not a recommendation impression.
select judgment_id, judgment_revision, query_id, query_family_id,
       near_duplicate_cluster_id, query_text, query_text_sha256,
       document_id, document_revision, chunk_id, chunk_text, chunk_text_sha256,
       assumeNotNull(relevance_grade) as relevance_grade, judged_mask,
       judgment_source, judgment_source_ref,
       judgment_source_hash, query_time, content_available_at, judged_at,
       available_at, revoked_at,
       multiIf(
         query_time >= parseDateTime64BestEffort('{{ var("splits")["train"]["start"] }}', 6, 'UTC')
         and query_time < parseDateTime64BestEffort('{{ var("splits")["train"]["end"] }}', 6, 'UTC'), 'train',
         query_time >= parseDateTime64BestEffort('{{ var("splits")["validation"]["start"] }}', 6, 'UTC')
         and query_time < parseDateTime64BestEffort('{{ var("splits")["validation"]["end"] }}', 6, 'UTC'), 'validation',
         query_time >= parseDateTime64BestEffort('{{ var("splits")["test"]["start"] }}', 6, 'UTC')
         and query_time < parseDateTime64BestEffort('{{ var("splits")["test"]["end"] }}', 6, 'UTC'), 'test',
         'out_of_range'
       ) as split
from {{ ref('dwd_qrel_revisions') }}
where _revision_rank = 1 and status = 'active'
  and relevance_grade is not null and judged_mask = true
