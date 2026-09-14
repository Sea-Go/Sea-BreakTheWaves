with detail as (
  select cohort, assignment_state, source_kind, source_coverage_state, coverage_receipt_sha256,
         impression_source_partition, definition_revision, label_revision, data_as_of,
         count() as visible,
         countIf(served_event_id != '') as served,
         countIf(evaluation_state = 'mature_positive') as positive,
         countIf(evaluation_state = 'mature_negative') as negative
  from {{ ref('ads_recommendation_exposures') }}
  group by cohort, assignment_state, source_kind, source_coverage_state, coverage_receipt_sha256,
           impression_source_partition, definition_revision, label_revision, data_as_of
)
select r.cohort, r.impression_source_partition
from {{ ref('ads_recommendation_quality') }} r
left join detail d on r.cohort=d.cohort and r.assignment_state=d.assignment_state
  and r.source_kind=d.source_kind and r.source_coverage_state=d.source_coverage_state
  and r.coverage_receipt_sha256=d.coverage_receipt_sha256
  and r.impression_source_partition=d.impression_source_partition
  and r.definition_revision=d.definition_revision and r.label_revision=d.label_revision
  and r.data_as_of=d.data_as_of
where d.cohort is null or r.visible_impressions != d.visible
  or r.served_visible_impressions != d.served
  or r.mature_positive_impressions != d.positive
  or r.mature_negative_impressions != d.negative
  or r.mature_evaluable_denominator != d.positive+d.negative
  or r.experiment_effect is not null
