select query_id, document_id, document_revision, chunk_id
from {{ ref('ds_search_qrels') }}
group by query_id, document_id, document_revision, chunk_id
having count() != 1
