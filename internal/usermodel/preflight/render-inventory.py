#!/usr/bin/env python3
"""Render the physical table/key inventory from the isolated PG16 oracle."""
import json
from pathlib import Path

root = Path(__file__).resolve().parent
contract = json.loads((root / 'contract.json').read_text())
tables = contract['catalog']['tables']
three = sum({'authority_id', 'tenant_id', 'subject_id'} <= {c['name'] for c in t['columns']} for t in tables)
assert len(tables) == 23 and three == 19

lines = [
    '# SubjectRef v2 阶段一：BTW usermodel 物理结构清单',
    '',
    '依据：BTW 开发集成提交 `f51723e0a1db3b5029342ef8207fb2255be79cec` 的七份 `migrations/usermodel/001..006` SQL，',
    '按部署顺序应用到隔离 PostgreSQL 16，再从 `pg_catalog` 读取真实列、主键、外键和唯一索引。',
    f'迁移源 SHA-256：`{contract["source_sha256"]}`。本文由 `render-inventory.py` 从 `contract.json` 生成。',
    '',
    '实际是 23 张表：19 张有完整 `(authority_id,tenant_id,subject_id)` 列，',
    '1 张未映射事件以外部主体为键，2 张 Ontology 表只按 authority/tenant 建键，',
    '1 张 coverage_prefix 没有主体列。若只数除 prefix 外的表为 22 张，但不能把 22 张都称作三字段主体表。',
    '',
    '| 表 | 全部物理列 | 主键 | 额外唯一键 | 外键 |',
    '| --- | --- | --- | --- | --- |',
]

for table in tables:
    columns = ', '.join(c['name'] for c in table['columns'])
    pk = next((c for c in table['constraints'] if c['kind'] == 'p'), None)
    pk_text = ', '.join(pk['columns']) if pk else '无'
    uniques = []
    for key in table['unique']:
        if pk and key['name'] == pk['name']:
            continue
        text = ', '.join(key['columns'])
        if key.get('predicate'):
            text += ' [partial: ' + key['predicate'] + ']'
        uniques.append(text)
    fks = []
    for key in table['constraints']:
        if key['kind'] != 'f':
            continue
        fks.append(', '.join(key['columns']) + ' → ' + key['ref_table'] + '(' + ', '.join(key['ref_columns']) + ')')
    lines.append('| `' + table['name'] + '` | ' + columns + ' | ' + pk_text + ' | ' + ('; '.join(uniques) or '无') + ' | ' + ('; '.join(fks) or '无') + ' |')

lines += [
    '',
    '清单中的“无 FK”表示当前旧 schema 未用物理 FK 强制该关系，不代表预检会跳过逻辑所有权：',
    '`outbox`、`watermarks`、`subject_bindings` 与已绑定 `unmapped_events` 的主体归属由只读查询另审。',
    'Serving `pair_id/approval_ref` 的 recommend 授权方不在本阶段 `migrations/usermodel` 范围内，',
    '本工具只校验 pointer 与所指 bundle 的主体和 pair 一致；外部批准真实性留待后续阶段。',
    '',
]
(root / 'SCHEMA_INVENTORY.md').write_text('\n'.join(lines))
