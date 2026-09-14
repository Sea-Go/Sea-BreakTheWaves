{% macro subject_equals(left, right) -%}
{{ left }}.authority_id = {{ right }}.authority_id
and {{ left }}.tenant_id = {{ right }}.tenant_id
and {{ left }}.subject_id = {{ right }}.subject_id
{%- endmacro %}
