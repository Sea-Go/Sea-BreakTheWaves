-- Fixed WS08-C SQL fixture: a warehouse-style reversible reading baseline.
-- $1/$2/$3 = complete SubjectRef; $4 = covered contiguous source position;
-- $5 = business as-of; $6 = information available-at. A real WS07-C DWS
-- generation must hand over this contribution identity plus its manifest.
WITH covered AS (
    SELECT e.producer,e.event_id,e.normalized_hash,e.source_sequence,
           e.event_body->>'value_ref' AS value_ref,e.occurred_at
    FROM usermodel_events e
    JOIN usermodel_active_facts af USING(authority_id,tenant_id,subject_id,producer,event_id)
    JOIN usermodel_outbox o ON o.authority_id=e.authority_id AND o.tenant_id=e.tenant_id
      AND o.subject_id=e.subject_id AND o.producer=e.producer AND o.event_id=e.event_id
      AND o.state_version=e.accepted_version AND o.event_type='usermodel.fact.accepted'
    WHERE e.authority_id=$1 AND e.tenant_id=$2 AND e.subject_id=$3
      AND e.producer='rtw.product' AND e.source_partition='product-1'
      AND e.source_sequence<=$4 AND e.status='accepted'
      AND e.semantic_kind='reading' AND e.event_body->>'predicate'='read'
      AND e.occurred_at<=$5 AND e.observed_at<=$6 AND o.created_at<=$6
)
SELECT producer,event_id,normalized_hash,source_sequence,
       COUNT(*) OVER () AS reading_count,
       FIRST_VALUE(value_ref) OVER (ORDER BY occurred_at DESC,producer DESC,event_id DESC) AS latest_value
FROM covered ORDER BY producer,event_id;
