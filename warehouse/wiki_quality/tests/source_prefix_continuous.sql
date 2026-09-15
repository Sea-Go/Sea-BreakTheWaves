-- A pinned generation must have exactly positions 1..cutoff, once each.
select producer
from {{ ref('dwd_wiki_source_prefix') }}
group by producer
having min(source_offset) != 1
   or max(source_offset) != {{ var('cutoff_offset') }}
   or count() != {{ var('cutoff_offset') }}
   or uniqExact(source_offset) != {{ var('cutoff_offset') }}
union all
select 'missing_prefix'
where (select count() from {{ ref('dwd_wiki_source_prefix') }}) = 0
