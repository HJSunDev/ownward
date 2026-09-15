"""Location-only repair of rejected organizations; semantic content stays intact."""
from __future__ import annotations

import copy
import json
import re

import semantic_representation as representation
from external_intelligence import ExternalIntelligenceError, validate_structured_output


INSTRUCTION = (
    "Correct the rejected evidence locations and source references using the supplied original material. "
    "The host preserves the existing retrieval metadata, units and relations; return only changed fields "
    "in corrections, keyed by the JSON pointers allowed by the schema. "
    "Preserve each relation's meaning, direction and conditions, and each object's identity and role. "
    "A unit's context contains original passages needed to support its mentions. Reference only declared "
    "units/mentions or use an original source selector. Selectors here use exact original text, with prefix "
    "or suffix when needed to disambiguate; they are not passage numbers. "
    "Use a path ending in /- to append only new context passages or missing object mentions; existing entries "
    "are retained automatically. To correct one mention's location, return its /selector field only. "
    "For example, corrections {\"/units/0/context/-\":[{\"exact\":\"Alice wrote the plan.\"}]} "
    "adds that original passage without rewriting other context. Keep existing objects and evidence. "
    "Do not omit supported mentions to bypass validation. If a source needs changes beyond these location "
    "fields, return an empty corrections object for it so the host can use its existing broader repair."
)


def fields(organization, error, owner_id):
    contract = representation.organization_contract()['output_schema']
    unit = contract['properties']['units']['items']['properties']
    result = {}
    units = {int(i) for i in re.findall(r'(?m)^units\[(\d+)\]', error)}
    endpoints = {(int(i), side) for i, side in re.findall(r'(?m)^links\[(\d+)\]\.(source|target):', error)}
    for i, side in endpoints:
        if i < len(organization.get('links', [])):
            endpoint = organization['links'][i][side]
            if endpoint.get('asset_id') == owner_id:
                units.update(j for j,u in enumerate(organization.get('units', [])) if u['id'] == endpoint.get('unit_id'))
    for i, item in enumerate(organization.get('units', [])):
        if i not in units:
            continue
        for key in ('context', 'mentions'):
            result[f'/units/{i}/{key}/-'] = copy.deepcopy(unit[key])
        for j in range(len(item.get('mentions', []))):
            result[f'/units/{i}/mentions/{j}/selector'] = copy.deepcopy(unit['mentions']['items']['properties']['selector'])
        if re.search(rf'(?m)^units\[{i}\].*(?:说明定位|说明原文)', error):
            result[f'/units/{i}/selector'] = copy.deepcopy(unit['selector'])
    branches = contract['properties']['links']['items']['anyOf']
    for i, link in enumerate(organization.get('links', [])):
        branch = next((b for b in branches if link['type'] in b['properties']['type'].get('enum', [])), None)
        if branch is None:
            continue
        for key in ('source', 'target'):
            if (i,key) in endpoints:
                rule = copy.deepcopy(branch['properties'][key])
                def constrain(node):
                    if isinstance(node,dict):
                        if 'asset_id' in node.get('properties',{}):
                            node['properties']['asset_id']={'enum':[link[key]['asset_id']]}
                        for v in node.values():constrain(v)
                    elif isinstance(node,list):
                        for v in node:constrain(v)
                constrain(rule)
                result[f'/links/{i}/{key}'] = rule
    return result


def request(work, feedback, semantic_contract):
    by_id = {row['work_id']: row for row in feedback}
    schemas = []
    rejected = []
    sources = {}
    for item in work:
        row = by_id[item['id']]
        organization = row['rejected_analysis']['organization']
        lines = row['error'].splitlines()
        if not lines or any(not (re.match(r'^(?:units\[\d+\].*|单元 ).*不在该单元或必要上下文内', line)
                                 or re.match(r'^units\[\d+\].*(?:说明定位与当前正文失配|说明原文不唯一)', line)
                                 or re.match(r'^links\[\d+\]\.(source|target):.*(?:未声明对象提及|说明定位与当前正文失配|说明原文不唯一)', line)) for line in lines):
            raise ExternalIntelligenceError('rejection needs broader semantic correction')
        # Context entries are preserved by local repair. A broken existing
        # entry requires replacement, which belongs to the full repair path.
        content = item['asset']['content']
        for unit in organization.get('units', []):
            for selector in unit.get('context', []):
                exact = selector.get('exact', '')
                quote = selector.get('prefix', '') + exact + selector.get('suffix', '')
                if not exact.strip() or representation._occurrences(content, quote) != 1:
                    raise ExternalIntelligenceError('existing context location needs broader semantic correction')
        allowed = fields(organization,row['error'],item['asset']['id'])
        if not allowed:
            raise ExternalIntelligenceError('rejection is not a localized reference error')
        needed = {item['asset']['id']}
        for pointer in allowed:
            if pointer.startswith('/links/'):
                _,_,index,side=pointer.split('/')
                needed.add(organization['links'][int(index)][side]['asset_id'])
        available = {a['id']: a for a in [item['asset'],*item.get('candidates',[])]}
        if not needed <= available.keys():
            raise ExternalIntelligenceError('rejected source is unavailable in frozen material')
        for identifier in sorted(needed):
            source=available[identifier]
            material={k:v for k,v in source.items() if k in ('id','revision','content','contexts','organization')}
            if identifier in sources and sources[identifier] != material:
                raise ExternalIntelligenceError('repair sources have conflicting frozen versions')
            sources[identifier]=material
        schemas.append({'type': 'object', 'additionalProperties': False,
            'required': ['work_id', 'corrections'], 'properties': {
                'work_id': {'enum': [item['id']]},
                'corrections': {'type': 'object', 'additionalProperties': False,
                                'properties': allowed}}})
        rejected.append({'work_id': item['id'], 'asset_id': item['asset']['id'], 'error': row['error'], 'organization': organization})
    schema = {'type': 'object', 'additionalProperties': False, 'required': ['repairs'],
        'properties': {'repairs': {'type': 'array', 'minItems': len(work), 'maxItems': len(work),
                                   'items': {'anyOf': schemas}}}}
    material = {'sources': list(sources.values())}
    prompt = INSTRUCTION + '\n\nOriginal material:\n' + json.dumps(material, ensure_ascii=False, separators=(',', ':'))
    prompt += '\n\nRejected organizations and errors:\n' + json.dumps(rejected, ensure_ascii=False, separators=(',', ':'))
    return prompt, schema


def apply(work, feedback, value, *, partial=False):
    expected = [item['id'] for item in work]
    repairs = value.get('repairs', [])
    supplied = [row.get('work_id') for row in repairs if isinstance(row,dict)]
    if ((not partial and (supplied != expected or len(repairs) != len(expected))) or len(supplied) != len(set(supplied))
            or supplied != [identifier for identifier in expected if identifier in supplied]):
        raise ExternalIntelligenceError('organization location repair reordered work')
    repairs_by_id = {row['work_id']:row for row in repairs if isinstance(row,dict) and 'work_id' in row}
    by_id = {row['work_id']: row for row in feedback}
    accepted, errors = {}, []
    for item in work:
        original = by_id[item['id']]['rejected_analysis']
        try:
            repair = repairs_by_id.get(item['id'])
            if repair is None:
                raise ExternalIntelligenceError('location correction incomplete')
            edits = repair['corrections']
            if not edits:
                raise ExternalIntelligenceError('location-only repair unavailable')
            paths = list(edits)
            if any(a != b and b.startswith(a + '/') for a in paths for b in paths):
                raise ExternalIntelligenceError('repair contains overlapping field edits')
            organization = copy.deepcopy(original['organization'])
            allowed = fields(organization,by_id[item['id']]['error'],item['asset']['id'])
            for pointer, replacement in edits.items():
                if pointer not in allowed:
                    raise ExternalIntelligenceError('repair changed a protected field')
                validate_structured_output(replacement, allowed[pointer])
                path = pointer.split('/')[1:]
                parent = organization
                for token in path[:-1]:
                    if isinstance(parent, dict) and token in ('context', 'mentions') and token not in parent:
                        parent[token] = []
                    parent = parent[int(token)] if isinstance(parent, list) else parent[token]
                if path[-1] == '-':
                    for item_value in replacement:
                        if path[-2] == 'mentions' and any(m.get('id') == item_value.get('id') and m != item_value for m in parent):
                            raise ExternalIntelligenceError('append cannot replace an existing object mention')
                        if item_value not in parent:
                            parent.append(copy.deepcopy(item_value))
                    continue
                key = int(path[-1]) if isinstance(parent, list) else path[-1]
                parent[key] = copy.deepcopy(replacement)
            for before, after in zip(original['organization'].get('units', []), organization.get('units', [])):
                if any(context not in after.get('context', []) for context in before.get('context', [])):
                    raise ExternalIntelligenceError('repair removed an existing context passage')
                mentions = {m['id']: m for m in after.get('mentions', [])}
                for old in before.get('mentions', []):
                    new = mentions.get(old['id'])
                    if new is None or {k:v for k,v in old.items() if k != 'selector'} != {k:v for k,v in new.items() if k != 'selector'}:
                        raise ExternalIntelligenceError('repair removed or changed an existing object mention')
            # Full semantic/reference validation is still performed by the kernel.
            check = copy.deepcopy(representation.organization_contract()['output_schema'])
            check['properties']['schema']['enum'] = [original['organization']['schema']]
            validate_structured_output(organization, check)
            references = by_id[item['id']].get('input_assets', representation.input_references(work))
            accepted[item['id']] = {**copy.deepcopy(original), 'organization': organization,
                                    'work_id': item['id'], 'input_assets': copy.deepcopy(references)}
        except (ExternalIntelligenceError, KeyError, TypeError, ValueError) as error:
            errors.append({'work_id': item['id'], 'error': str(error)})
    return accepted, errors
