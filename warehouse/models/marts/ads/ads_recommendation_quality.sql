-- Counts remain available when a ratio/experiment is not evaluable. No NULL
-- source, pending label or unserved item is turned into a negative or zero CTR.
select cohort, assignment_state, source_kind, source_coverage_state, coverage_receipt_sha256,
       impression_source_partition, definition_revision, label_revision, data_as_of,
       count() as visible_impressions,
       countIf(served_event_id != '') as served_visible_impressions,
       countIf(qualification = 'eligible') as eligible_visible_impressions,
       countIf(evaluation_state = 'mature_positive') as mature_positive_impressions,
       countIf(evaluation_state = 'mature_negative') as mature_negative_impressions,
       countIf(evaluation_state = 'pending') as pending_impressions,
       countIf(startsWith(evaluation_state, 'excluded:')) as excluded_impressions,
       countIf(evaluation_state in ('source_unavailable', 'positive_source_unavailable',
                                    'coverage_unverified', 'label_conflict')) as source_or_label_gap_impressions,
       countIf(evaluation_state in ('mature_positive', 'mature_negative')) as mature_evaluable_denominator,
       'not_evaluable_assignment_missing' as experiment_state,
       CAST(NULL, 'Nullable(Float64)') as experiment_effect
from {{ ref('ads_recommendation_exposures') }}
group by cohort, assignment_state, source_kind, source_coverage_state, coverage_receipt_sha256,
         impression_source_partition, definition_revision, label_revision, data_as_of
