"""Independent Draft 2020-12 and semantic validation; no network or media requests.

Accepts one response per JSON file, or a JSON array of responses from adapter
exports. Diagnostics deliberately omit instance values (they may be signed URLs).
"""
import argparse
from datetime import datetime
import json
from pathlib import Path
import re
import subprocess
from urllib.parse import unquote, urlsplit

from jsonschema import Draft202012Validator, FormatChecker

ROOT = Path(__file__).resolve().parents[1]
SCHEMA = json.loads((ROOT / 'docs/schema/unified-v1/unified-v1.schema.json').read_text(encoding='utf-8'))
Draft202012Validator.check_schema(SCHEMA)
# The contract has only local references. Dropping the identifier for validation
# keeps older jsonschema versions from resolving a URN as a network location;
# all validation keywords and local definitions remain unchanged.
STRUCTURE = Draft202012Validator({k: v for k, v in SCHEMA.items() if k != '$id'}, format_checker=FormatChecker())
TECHNICAL = {'width', 'height', 'duration_ms', 'fps', 'bitrate_bps', 'video_bitrate_bps',
             'audio_bitrate_bps', 'has_audio', 'video_codec', 'audio_codec', 'audio_language',
             'audio_channels', 'sample_rate_hz', 'hdr_type', 'audio_language_role'}
AUDIO = {'audio_bitrate_bps', 'audio_codec', 'audio_language', 'audio_language_role', 'audio_channels', 'sample_rate_hz'}
VIDEO = {'width', 'height', 'fps', 'hdr_type', 'video_codec', 'video_bitrate_bps'}


def require(condition, message):
    if not condition:
        raise ValueError(message)


def moment(value):
    return datetime.fromisoformat(value.replace('Z', '+00:00'))


def unique(values, scope):
    require(len(values) == len(set(values)), 'duplicate identity: ' + scope)


def validate_url(value, relay=False):
    require(not re.search(r'[\s\x00-\x1f\x7f\\]', value), 'unsafe URL characters')
    parsed = urlsplit(value)
    require(not parsed.fragment, 'URL fragment is not a media request')
    if relay:
        require(not parsed.scheme and not parsed.netloc and parsed.path.startswith('/api/'), 'relay must use an API-relative path')
        decoded = unquote(parsed.path)
        require(not re.search(r'[\s\x00-\x1f\x7f\\]', decoded), 'unsafe decoded relay path')
        require(not any(p in {'.', '..'} for p in decoded.split('/')), 'relay traversal')
        require(not re.search(r'%2e|%2f|%5c|%25', decoded, re.I), 'nested relay path encoding')
    else:
        require(parsed.scheme.lower() in {'http', 'https'} and bool(parsed.hostname), 'direct URL must be HTTP(S)')
        require(parsed.username is None and parsed.password is None, 'URL credentials are not public metadata')
        try:
            parsed.port
        except ValueError as error:
            raise ValueError('invalid URL port') from error


def walk(value):
    if isinstance(value, dict):
        yield value
        for child in value.values():
            yield from walk(child)
    elif isinstance(value, list):
        for child in value:
            yield from walk(child)


def eligible(source, now):
    return source.get('availability') != 'unavailable' and (
        'expires_at' not in source or (now is not None and moment(source['expires_at']) > now))


def sources_valid(sources):
    ids = [s['id'] for s in sources if 'id' in s]
    unique(ids, 'source')
    require(len(sources) == 1 or len(ids) == len(sources), 'multiple sources require IDs')
    for source in sources:
        validate_url(source['url'], source.get('access', 'direct') == 'relay')
        if 'fallback_urls' in source:
            primary = urlsplit(source['url'])
            require(source.get('access', 'direct') != 'relay' and primary.scheme == 'https',
                    'credential fallbacks require a direct HTTPS source')
            # Host case and an explicit default port do not change the origin.
            origin = (primary.hostname, primary.port if primary.port is not None else 443)
            seen = {source['url']}
            for alternate in source['fallback_urls']:
                validate_url(alternate)
                parsed = urlsplit(alternate)
                require(parsed.scheme == 'https' and
                        (parsed.hostname, parsed.port if parsed.port is not None else 443) == origin,
                        'credential fallback must have the same HTTPS origin')
                require(alternate not in seen, 'duplicate credential fallback URL')
                seen.add(alternate)


def requirements(delivery):
    kind = delivery['kind']
    if kind == 'file':
        return set()
    if kind in {'hls', 'dash'}:
        return {kind}
    if kind == 'segments':
        return {'segment_' + delivery['join']}
    return {kind}.union(*(requirements(r['delivery']) for r in delivery['resources']))


def usable(delivery, now):
    if delivery['kind'] in {'file', 'hls', 'dash'}:
        return any(eligible(s, now) for s in delivery['sources'])
    if delivery['kind'] == 'segments':
        chunks = delivery['pieces'] + ([delivery['init']] if 'init' in delivery else [])
        return all(any(eligible(s, now) for s in chunk['sources']) for chunk in chunks)
    return all(usable(r['delivery'], now) for r in delivery['resources'])


def dimensions(variant):
    m = variant.get('metadata', {})
    if variant['delivery']['kind'] == 'mux':
        video = next((r.get('metadata', {}) for r in variant['delivery']['resources'] if r['role'] == 'video'), {})
        merged = {**video, **m}
        for field in ('width', 'height'):
            if (field in video.get('field_conflicts', {}) or field in m.get('field_conflicts', {})
                    or (field in video and field in m and video[field] != m[field])):
                merged.pop(field, None)
        m = merged
    if 'width' in m and 'height' in m and not {'width', 'height'}.intersection(m.get('field_conflicts', {})):
        return sorted((m['width'], m['height']))
    return None


def higher(left, right):
    a, b = dimensions(left), dimensions(right)
    if not a or not b:
        return False
    ratio_a, ratio_b = a[1] / a[0], b[1] / b[0]
    return abs(ratio_a / ratio_b - 1) <= .01 and all(x >= y for x, y in zip(a, b)) and a != b


def split_video_only_file(variant):
    meta = variant.get('metadata', {})
    if meta.get('has_audio') is not False:
        return False
    return (any(meta.get(field) is not None for field in ('video_codec', 'container', 'mime_type'))
            or '仅视频无声' in (variant.get('label') or ''))


def audio_only_file(variant):
    meta = variant.get('metadata', {})
    mime = meta.get('mime_type') or ''
    if isinstance(mime, str) and mime.startswith('audio/'):
        return True
    return (meta.get('audio_codec') is not None
            and all(meta.get(field) is None for field in ('video_codec', 'width', 'height')))


def default_eligible_files(item_type, files):
    if item_type != 'video':
        return files
    audible_complete = any(
        not split_video_only_file(v) and not audio_only_file(v) and v.get('metadata', {}).get('has_audio') is True
        for v in files
    )
    if audible_complete:
        return [v for v in files if not split_video_only_file(v) and not audio_only_file(v)]
    non_audio = [v for v in files if not audio_only_file(v)]
    return non_audio if non_audio else files


def metadata_valid(metadata, role=None):
    conflicts = metadata.get('field_conflicts', {})
    definitions = SCHEMA['$defs']['MediaMetadata']['properties']
    for field, facts in conflicts.items():
        require(field in definitions and field not in {'evidence', 'field_evidence', 'field_conflicts'}, 'unknown metadata conflict field')
        require(field not in metadata, 'conflicting metadata cannot claim a scalar fact')
        values = set()
        for fact in facts:
            require(not list(Draft202012Validator(definitions[field]).iter_errors(fact['value'])), 'conflict value does not match field type')
            if field == 'audio_language':
                require(bool(fact['value'].strip()), 'language observation must not be blank')
            if field == 'has_audio' and role in {'image', 'subtitle', 'video'}:
                require(fact['value'] is False, 'resource role conflicts with embedded audio')
            if field == 'mime_type' and role in {'image', 'audio'}:
                require(fact['value'].startswith(role + '/'), 'MIME conflicts with media role')
            # JSON numbers 30 and 30.0 express the same observation, including
            # when their evidence differs. Match PHP and browser equality.
            value = fact['value']
            category = 'boolean' if isinstance(value, bool) else 'number' if isinstance(value, (int, float)) else 'string'
            values.add((category, value))
        require(len(values) == len(facts) and len(values) >= 2, 'conflicts need distinct values')
    present = set(metadata).union(conflicts)
    if 'audio_language' in metadata:
        require(bool(metadata['audio_language'].strip()), 'language must not be blank')
    if 'audio_language_role' in present:
        require('audio_language' in present, 'language role requires an explicit language')
    known = TECHNICAL.intersection(metadata)
    evidence = metadata.get('evidence', 'unknown')
    require(not known or evidence in {'probe', 'upstream', 'mixed'}, 'technical fields require evidence')
    fields = metadata.get('field_evidence', {})
    if evidence == 'mixed':
        known_format = {'container', 'extension', 'mime_type'}.intersection(metadata)
        require(known <= set(fields) <= known.union(known_format), 'mixed evidence must cover known technical fields and only present format fields')
    else:
        require(not fields, 'field evidence only belongs to mixed metadata')
    if metadata.get('has_audio') is False:
        require(not AUDIO.intersection(present), 'silent media cannot have audio fields')
    if role in {'image', 'subtitle', 'video'}:
        require(metadata.get('has_audio') is not True and not AUDIO.intersection(present), 'resource role conflicts with embedded audio')
    if role == 'audio':
        require(not VIDEO.intersection(present), 'audio resource has video metadata')
    mime = metadata.get('mime_type', '')
    if role in {'image', 'audio'} and mime:
        require(mime.startswith(role + '/'), 'MIME conflicts with media role')


def delivery_valid(delivery, metadata=None):
    kind = delivery['kind']
    if kind in {'file', 'hls', 'dash'}:
        sources_valid(delivery['sources'])
        if kind in {'hls', 'dash'}:
            if 'selection' in delivery:
                require(bool(delivery['selection']), 'empty manifest selection')
            if delivery['scope'] == 'adaptive' and not delivery.get('selection'):
                require(not VIDEO.intersection(metadata or {}), 'unselected adaptive manifest cannot declare fixed video metadata')
        return
    if kind == 'segments':
        require([p['index'] for p in delivery['pieces']] == list(range(1, len(delivery['pieces']) + 1)), 'segment sequence must be continuous')
        if delivery['join'] == 'byte_concat':
            require(delivery.get('join_evidence') == 'validated_fragment_layout', 'byte concatenation requires layout evidence')
        for part in delivery['pieces'] + ([delivery['init']] if 'init' in delivery else []):
            sources_valid(part['sources'])
            if 'byte_range' in part:
                require(part['byte_range']['start'] <= part['byte_range']['end_inclusive'], 'reversed byte range')
        return
    resources = delivery['resources']
    unique([r['id'] for r in resources], 'resource')
    roles = [r['role'] for r in resources]
    if kind == 'pair':
        require(sorted(roles) == ['image', 'motion'], 'pair requires one image and one motion')
    else:
        require(roles.count('video') == 1 and roles.count('audio') >= 1 and set(roles) <= {'video', 'audio'}, 'mux requires a video and audio resources')
        # Conflicting summaries stay observable; dimensions() excludes disputed
        # geometry and the public policy conservatively retains the candidate.
    for resource in resources:
        metadata_valid(resource.get('metadata', {}), resource['role'])
        delivery_valid(resource['delivery'], resource.get('metadata'))


def validate_semantics(response):
    if response['code'] != 'OK':
        require(response['error']['code'] == response['code'], 'error envelope code differs')
        if 'retry_after_seconds' in response['error']:
            require(response['error'].get('retryable') is True, 'retry delay requires retryable error')
        return
    data = response['data']
    now = moment(data['generated_at']) if 'generated_at' in data else None
    all_sources = [o for o in walk(data) if 'url' in o and ('variant_id' not in o)]
    for source in all_sources:
        # Author URLs are plain direct links; source access defaults to direct.
        validate_url(source['url'], source.get('access', 'direct') == 'relay')
        require('expires_at' not in source or now is not None, 'expiry requires generated_at')
    items = data['items']
    ids = [i['id'] for i in items]
    unique(ids, 'item')
    require(len(items) == 1 or 'default_item_id' in data, 'multiple items require an entry item')
    require('default_item_id' not in data or data['default_item_id'] in ids, 'entry item missing')
    if 'source_url' in data:
        validate_url(data['source_url'])
    for group in data.get('groups', []):
        unique(group['item_ids'], 'group member')
        require(set(group['item_ids']) <= set(ids), 'group references missing item')
        require(group.get('total_count', len(group['item_ids'])) >= len(group['item_ids']), 'group total smaller than its members')
    unique([g['id'] for g in data.get('groups', [])], 'group')
    for warning in response.get('warnings', []):
        require('item_id' not in warning or warning['item_id'] in ids, 'warning references missing item')
    for item in items:
        if 'resolve_url' in item:
            validate_url(item['resolve_url'], True)
        if 'source_url' in item:
            validate_url(item['source_url'])
        if item.get('state', 'ready') != 'ready':
            require(item['default'] is None, 'unresolved item has a default')
            continue
        all_variants = item['variants']
        unique([v['id'] for v in all_variants], 'variant')
        variants = [v for v in all_variants if v.get('state') != 'deferred']
        pending = len(variants) != len(all_variants)
        for variant in variants:
            role = 'audio' if item['type'] == 'audio' else 'image' if item['type'] == 'image' else None
            metadata_valid(variant.get('metadata', {}), role)
            delivery_valid(variant['delivery'], variant.get('metadata'))
            require((variant['delivery']['kind'] == 'pair') == (item['type'] == 'live_photo'), 'pair and live-photo type must agree')
        usable_files = [v for v in variants if v['delivery']['kind'] == 'file' and usable(v['delivery'], now)]
        files = default_eligible_files(item['type'], usable_files)
        selected = item['default']
        if selected is None:
            require(not files, 'null default hides an eligible complete file')
            require('default_unavailable_reason' in item, 'null default requires reason')
            require(not item.get('higher_quality_variants'), 'higher variants require a default baseline')
            reason = item['default_unavailable_reason']
            available_processing = any(usable(v['delivery'], now) for v in variants)
            require((reason == 'processing_required') == available_processing, 'null reason does not match available processing')
            require((reason == 'needs_resolve') == (pending and not available_processing), 'deferred variants require an honest pending reason when no usable delivery exists')
            if reason == 'needs_refresh':
                require(any('expires_at' in s and moment(s['expires_at']) <= now for s in walk(variants)), 'refresh reason lacks expiry')
            continue
        require('default_unavailable_reason' not in item, 'non-null default has an unavailable reason')
        variant = next((v for v in files if v['id'] == selected['variant_id']), None)
        require(variant is not None, 'default does not identify an eligible complete file')
        sources = variant['delivery']['sources']
        source = next((s for s in sources if s.get('id') == selected.get('source_id')), None) if 'source_id' in selected else sources[0] if len(sources) == 1 else None
        require(source is not None and eligible(source, now), 'default source missing or expired')
        require(source['url'] == selected['url'], 'default URL differs from selected source')
        require(source.get('access', 'direct') == selected.get('access', 'direct'), 'default access differs')
        require(source.get('expires_at') == selected.get('expires_at'), 'default expiry differs')
        require(not any(higher(v, variant) for v in files), 'default omits a higher eligible file')
        expected = {v['id']: requirements(v['delivery']) for v in variants if requirements(v['delivery']) and usable(v['delivery'], now) and higher(v, variant)}
        declared = item.get('higher_quality_variants', [])
        unique([v['variant_id'] for v in declared], 'higher variant')
        require(set(expected) == {v['variant_id'] for v in declared}, 'higher-quality references differ from eligible higher processing versions')
        for reference in declared:
            require(set(reference['requires']) == expected[reference['variant_id']] and len(reference['requires']) == len(set(reference['requires'])), 'higher-quality capabilities differ from delivery')
    unique([a['id'] for a in data.get('attachments', [])], 'attachment')
    for attachment in data.get('attachments', []):
        resource = attachment['resource']
        metadata_valid(resource.get('metadata', {}), resource['role'])
        delivery_valid(resource['delivery'], resource.get('metadata'))


def validate(response):
    errors = list(STRUCTURE.iter_errors(response))
    if errors:
        # Never format jsonschema's full error: it embeds signed instance values.
        raise ValueError('JSON Schema rejected response (' + str(len(errors)) + ' structural errors)')
    validate_semantics(response)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('inputs', nargs='*', type=Path)
    parser.add_argument('--php', type=Path, help='Also export each registered actual adapter using offline synthetic parser fixtures')
    parser.add_argument('--output', type=Path)
    args = parser.parse_args()
    if not args.inputs and not args.php:
        parser.error('Provide JSON files or --php for the actual adapter fixtures')
    records = []
    responses = []
    for path in args.inputs:
        value = json.loads(path.read_text(encoding='utf-8-sig'))
        for index, response in enumerate(value if isinstance(value, list) else [value]):
            responses.append((str(path), index, response))
    if args.php:
        registry = json.loads((ROOT / 'backend/config/platforms.json').read_text(encoding='utf-8'))
        for platform in registry:
            label = 'actual-adapter/synthetic/' + platform['id']
            try:
                exported = subprocess.run([str(args.php.resolve()), str(ROOT / 'backend/tests/unified-export.php'), platform['id']],
                                          capture_output=True, text=True, encoding='utf-8', timeout=30, cwd=ROOT)
                if exported.returncode:
                    raise ValueError('Adapter fixture export failed; run that platform export directly for diagnostics')
                responses.append((label, 0, json.loads(exported.stdout)))
            except (OSError, subprocess.TimeoutExpired, ValueError) as error:
                records.append({'file': label, 'index': 0, 'passed': False, 'reason': type(error).__name__})
    for label, index, response in responses:
        try:
            validate(response)
            records.append({'file': label, 'index': index, 'passed': True})
        except (ValueError, TypeError, KeyError) as error:
            records.append({'file': label, 'index': index, 'passed': False, 'reason': str(error)})
    result = {'schema': SCHEMA['$id'], 'validator': 'jsonschema Draft202012Validator + independent semantics', 'total': len(records), 'passed': sum(r['passed'] for r in records), 'checks': records, 'evidence_scope': 'Structure and semantics only; does not prove upstream availability or browser saving.'}
    if args.output:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(json.dumps(result, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
    print(json.dumps(result, ensure_ascii=True))
    return 0 if result['passed'] == result['total'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
