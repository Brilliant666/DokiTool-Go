"""Independent contract examples and adversarial checks; all inputs synthetic."""
from copy import deepcopy
import importlib.util
import json
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('unified_validation', ROOT / 'scripts/validate-unified-schema.py')
contract = importlib.util.module_from_spec(spec)
spec.loader.exec_module(contract)
EXAMPLES = ROOT / 'docs/schema/unified-v1/examples'


def example(number):
    return json.loads(next(EXAMPLES.glob(f'{number:02}-*.json')).read_text(encoding='utf-8'))


def item(response):
    return response['data']['items'][0]


def variant(response):
    return item(response)['variants'][0]


class ContractTests(unittest.TestCase):
    def test_deferred_variants_never_promise_a_file_or_measured_4k(self):
        response = example(27)
        contract.validate(response)
        pending = item(response)['variants'][-1]
        for key, value in [('delivery', {'kind': 'file', 'sources': [{'url': 'https://media.example.test/false.mp4'}]}), ('metadata', {'width': 3840, 'height': 2160, 'evidence': 'probe'})]:
            changed = deepcopy(response)
            item(changed)['variants'][-1][key] = value
            with self.assertRaises(ValueError):
                contract.validate(changed)
        changed = deepcopy(response)
        item(changed)['default']['variant_id'] = pending['id']
        with self.assertRaises(ValueError):
            contract.validate(changed)
        item(response)['variants'] = [pending]
        item(response)['default'] = None
        item(response)['default_unavailable_reason'] = 'needs_resolve'
        contract.validate(response)

    def rejects(self, number, mutate):
        response = example(number)
        mutate(response)
        with self.assertRaises(ValueError):
            contract.validate(response)

    def test_all_design_examples_independently_validate(self):
        for path in sorted(EXAMPLES.glob('*.json')):
            with self.subTest(path=path.name):
                contract.validate(json.loads(path.read_text(encoding='utf-8')))

    def test_structure_rejects_old_and_unknown_shapes(self):
        changes = [
            lambda r: r.update(schema_version='0.9'),
            lambda r: r.update(code=200),
            lambda r: r['data'].update(url='https://media.example.test/old.mp4'),
            lambda r: r['data'].update(video_backup=[]),
            lambda r: item(r).pop('default'),
            lambda r: item(r).update(id=123),
            lambda r: variant(r).update(resources=[]),
            lambda r: variant(r)['delivery'].update(kind='mystery'),
            lambda r: variant(r)['delivery']['sources'][0].update(headers={'Cookie': 'placeholder'}),
            lambda r: variant(r)['delivery']['sources'][0].update(size_bytes=0),
        ]
        for index, change in enumerate(changes):
            with self.subTest(index=index):
                self.rejects(2, change)

    def test_default_is_an_exact_local_file_reference(self):
        changes = [
            lambda r: item(r)['default'].update(variant_id='missing'),
            lambda r: item(r)['default'].update(url='https://media.example.test/another.mp4'),
            lambda r: item(r)['default'].update(access='relay'),
            lambda r: item(r)['default'].update(expires_at='2099-01-01T00:00:00Z'),
            lambda r: item(r).update(default=None, default_unavailable_reason='unavailable'),
            lambda r: item(r).update(default_unavailable_reason='unavailable'),
            lambda r: variant(r)['delivery']['sources'].append({'url': 'https://media.example.test/backup.mp4'}),
            lambda r: variant(r)['delivery']['sources'][0].update(availability='unavailable'),
            lambda r: variant(r)['delivery']['sources'][0].update(expires_at='2000-01-01T00:00:00Z'),
            lambda r: r['data'].update(default_item_id='missing'),
        ]
        for index, change in enumerate(changes):
            with self.subTest(index=index):
                self.rejects(2, change)
        self.rejects(16, lambda r: item(r)['default'].update(variant_id='4k-mux'))
        self.rejects(16, lambda r: item(r).pop('higher_quality_variants'))
        self.rejects(16, lambda r: item(r)['higher_quality_variants'][0].update(requires=['dash']))
        self.rejects(16, lambda r: item(r)['higher_quality_variants'].append(deepcopy(item(r)['higher_quality_variants'][0])))
        self.rejects(16, lambda r: item(r)['variants'][1]['delivery']['resources'][1]['delivery']['sources'][0].update(availability='unavailable'))

    def test_known_higher_file_cannot_be_hidden(self):
        r = example(16)
        high = deepcopy(variant(r))
        high.update(id='4k-file', metadata={'width': 3840, 'height': 2160, 'evidence': 'probe'})
        high['delivery']['sources'][0]['url'] = 'https://media.example.test/high.mp4'
        item(r)['variants'].append(high)
        with self.assertRaises(ValueError):
            contract.validate(r)

    def test_source_identity_expiry_and_null_reason(self):
        self.rejects(18, lambda r: r['data'].pop('generated_at'))
        self.rejects(18, lambda r: item(r)['default'].pop('source_id'))
        self.rejects(21, lambda r: item(r).update(default_unavailable_reason='processing_required'))
        self.rejects(22, lambda r: item(r).update(default_unavailable_reason='needs_refresh'))
        self.rejects(17, lambda r: item(r).update(default_unavailable_reason='needs_resolve'))

    def test_quality_and_composition_are_not_labels(self):
        self.rejects(16, lambda r: variant(r)['metadata'].pop('evidence'))
        self.rejects(16, lambda r: variant(r)['metadata'].update(evidence='mixed', field_evidence={'width': 'probe'}))
        self.rejects(16, lambda r: item(r)['variants'][1]['delivery']['resources'][0]['metadata'].update(has_audio=True))
        self.rejects(16, lambda r: item(r)['variants'][1].update(metadata={'width': 1280, 'height': 720, 'evidence': 'upstream'}))
        self.rejects(14, lambda r: variant(r).update(metadata={'width': 3840, 'height': 2160, 'evidence': 'upstream'}))
        self.rejects(11, lambda r: variant(r)['metadata'].update(mime_type='video/mp4'))

    def test_query_bytes_preserved_and_access_restricted(self):
        for url in ['https://user:pass@media.example.test/v', 'https://media.example.test/v#part', 'https://media.example.test/\\v', 'file:///tmp/video.mp4']:
            with self.subTest(url=url):
                def change(r):
                    variant(r)['delivery']['sources'][0]['url'] = url
                    item(r)['default']['url'] = url
                self.rejects(2, change)
        r = example(2)
        url = 'https://media.example.test/v.mp4?sig=abc%23def%2Bghi%2Fj%3D'
        variant(r)['delivery']['sources'][0]['url'] = url
        item(r)['default']['url'] = url
        contract.validate(r)
        self.assertEqual(item(r)['default']['url'], url)

    def test_credential_fallbacks_accept_same_https_origin(self):
        for primary, alternatives in [
            ('https://media.example.test/video.mp4?signature=old',
             ['https://media.example.test/video.mp4?signature=new']),
            ('https://MEDIA.EXAMPLE.TEST:443/video.mp4',
             ['https://media.example.test/other.mp4?sig=abc%23def',
              'https://media.example.test:443/video.mp4?signature=new']),
            ('https://media.example.test:8443/video.mp4',
             ['https://MEDIA.EXAMPLE.TEST:8443/other.mp4']),
        ]:
            with self.subTest(primary=primary):
                response = example(2)
                source = variant(response)['delivery']['sources'][0]
                source.update(url=primary, fallback_urls=alternatives)
                item(response)['default']['url'] = primary
                unchanged = deepcopy(response)
                contract.validate(response)
                self.assertEqual(response, unchanged)

    def test_credential_fallbacks_reject_invalid_candidates(self):
        primary = 'https://v3-cdn-tos.ppxvod.com/video.mp4?signature=old'
        cases = {
            'cross-origin': ['https://other.example.test/other.mp4'],
            'http-downgrade': ['http://v3-cdn-tos.ppxvod.com/other.mp4'],
            'fragment': ['https://v3-cdn-tos.ppxvod.com/other.mp4#fragment'],
            'userinfo': ['https://user:pass@v3-cdn-tos.ppxvod.com/other.mp4'],
            'primary-duplicate': [primary],
            'different-port': ['https://v3-cdn-tos.ppxvod.com:8443/other.mp4'],
            'invalid-port': ['https://v3-cdn-tos.ppxvod.com:99999/other.mp4'],
            'duplicate-candidates': [primary + 'x', primary + 'x'],
            'empty': [],
            'too-many': [primary + str(i) for i in range(3)],
            'not-array': primary,
        }
        for label, alternatives in cases.items():
            with self.subTest(case=label):
                response = example(2)
                variant(response)['delivery']['sources'][0].update(
                    url=primary, fallback_urls=alternatives)
                item(response)['default']['url'] = primary
                with self.assertRaises(ValueError):
                    contract.validate(response)

    def test_credential_fallbacks_require_direct_https_primary(self):
        for primary, access in [('http://media.example.test/video.mp4', 'direct'),
                                ('/api/media/video', 'relay')]:
            with self.subTest(access=access):
                response = example(2)
                variant(response)['delivery']['sources'][0].update(
                    url=primary, access=access,
                    fallback_urls=['https://media.example.test/other.mp4'])
                item(response)['default'].update(url=primary, access=access)
                with self.assertRaises(ValueError):
                    contract.validate(response)

    def test_reordering_and_browser_fetch_do_not_reselect(self):
        r = example(18)
        selected = deepcopy(item(r)['default'])
        item(r)['variants'].reverse()
        for v in item(r)['variants']:
            v['delivery']['sources'].reverse()
            for s in v['delivery']['sources']:
                s['browser_fetch'] = 'blocked'
        contract.validate(r)
        self.assertEqual(item(r)['default'], selected)

    def test_protected_capabilities_require_known_enums_and_field_evidence(self):
        r = example(28)
        contract.validate(r)
        for update in [
            {'hdr_type': 'HDR'}, {'hdr_type': 'hdr10', 'evidence': 'unknown'},
            {'hdr_type': 'hdr10', 'evidence': 'mixed', 'field_evidence': {'width': 'upstream', 'height': 'upstream'}},
        ]:
            changed = deepcopy(r)
            item(changed)['variants'][1]['delivery']['resources'][0]['metadata'].update(update)
            with self.assertRaises(ValueError):
                contract.validate(changed)
        for update in [{'audio_language_role': 'dub'}, {'audio_language': ''}, {'audio_language_role': 'alternate', 'evidence': 'unknown'}]:
            changed = deepcopy(r)
            item(changed)['variants'][1]['delivery']['resources'][1]['metadata'].update(update)
            with self.assertRaises(ValueError):
                contract.validate(changed)
        changed = deepcopy(r)
        item(changed)['variants'][1]['delivery']['resources'][1]['metadata'].pop('audio_language')
        with self.assertRaises(ValueError):
            contract.validate(changed)

    def test_hdr_fps_language_channels_do_not_invent_higher_spatial_quality(self):
        r = example(28)
        contract.validate(r)
        item(r)['higher_quality_variants'] = [{'variant_id': item(r)['variants'][1]['id'], 'requires': ['mux']}]
        with self.assertRaises(ValueError):
            contract.validate(r)

    def test_conflicting_resource_evidence_is_retained_without_false_spatial_order(self):
        r = example(29)
        contract.validate(r)
        self.assertIsNone(contract.dimensions(item(r)['variants'][1]))
        item(r)['higher_quality_variants'] = [{'variant_id': item(r)['variants'][1]['id'], 'requires': ['mux']}]
        with self.assertRaises(ValueError):
            contract.validate(r)

    def test_merged_identity_conflicts_retain_values_and_do_not_invent_geometry(self):
        facts = lambda *values: [{'value': value, 'evidence': 'upstream'} for value in values]
        r = example(28)
        mux = item(r)['variants'][1]
        mux['metadata'] = {'field_conflicts': {'fps': facts(30, 60), 'hdr_type': facts('sdr', 'hdr10')}}
        video = mux['delivery']['resources'][0]['metadata']
        video.pop('width')
        video['field_conflicts'] = {'width': facts(1920, 3840)}
        audio = mux['delivery']['resources'][1]['metadata']
        audio.pop('audio_language')
        audio['field_conflicts'] = {'audio_language': facts('en', 'fr')}
        contract.validate(r)
        self.assertIsNone(contract.dimensions(mux))
        for update in [
            {'fps': facts(60, 60)}, {'fps': facts(60)}, {'fps': facts('30', '60')},
            {'fps': [{'value': 30, 'evidence': 'unknown'}, {'value': 60, 'evidence': 'probe'}]},
            {'hdr_type': facts('sdr', 'HEVC')}, {'unknown': facts(1, 2)},
            {'audio_language': facts('   ', 'en')},
            {'fps': [{'value': 30, 'evidence': 'upstream'}, {'value': 30.0, 'evidence': 'probe'}]},
        ]:
            changed = deepcopy(r)
            item(changed)['variants'][1]['metadata']['field_conflicts'] = update
            with self.assertRaises(ValueError):
                contract.validate(changed)
        changed = deepcopy(r)
        item(changed)['variants'][1]['metadata']['fps'] = 60
        with self.assertRaises(ValueError):
            contract.validate(changed)
        changed = deepcopy(r)
        item(changed)['variants'][1]['delivery']['resources'][1]['metadata'] = {'audio_language': '   ', 'audio_language_role': 'main', 'evidence': 'upstream'}
        with self.assertRaises(ValueError):
            contract.validate(changed)
        changed = deepcopy(r)
        item(changed)['variants'][1]['delivery']['resources'][1]['metadata']['field_conflicts']['fps'] = facts(30, 60)
        with self.assertRaises(ValueError):
            contract.validate(changed)
        changed = deepcopy(r)
        item(changed)['variants'][1]['delivery']['resources'][0]['metadata']['field_conflicts']['has_audio'] = facts(False, True)
        with self.assertRaises(ValueError):
            contract.validate(changed)
        changed = deepcopy(r)
        item(changed)['higher_quality_variants'] = [{'variant_id': mux['id'], 'requires': ['mux']}]
        with self.assertRaises(ValueError):
            contract.validate(changed)


if __name__ == '__main__':
    unittest.main(verbosity=2)
