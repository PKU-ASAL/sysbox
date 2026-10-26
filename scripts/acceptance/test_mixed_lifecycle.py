import copy
import unittest
from mixed_lifecycle import verify_states, verify_translated_sources


class MixedEvidenceTests(unittest.TestCase):
    def fixture(self):
        resources = []
        for name in ('docker', 'firecracker', 'libvirt'):
            resources.append(dict(address='sysbox_node.' + name, resource_type='sysbox_node',
                                  driver=name, owner_uid='owner', resource_uid=name,
                                  external_id='old-' + name, generation=1,
                                  attachments=[dict(name='matrix', mac='mac-' + name,
                                                    ip_prefixes=['10.49.0.2/24'])]))
        resources.append(dict(address='sysbox_network.matrix', resource_type='sysbox_network',
                              owner_uid='owner', resource_uid='network', external_id='network', generation=1))
        before = dict(topology_uid='owner', resources=resources)
        after = copy.deepcopy(before)
        for item in after['resources'][:3]:
            item.update(external_id='new-' + item['driver'], generation=2)
        return before, after

    def test_all_three_generations_and_network_must_be_preserved(self):
        before, after = self.fixture()
        verify_states(before, after)
        for mutate in (
            lambda s: s['resources'].pop(0),
            lambda s: s['resources'].append(copy.deepcopy(s['resources'][0])),
            lambda s: s['resources'][0].update(generation=1),
            lambda s: s['resources'][1].update(resource_uid='other'),
            lambda s: s['resources'][-1].update(external_id='replaced'),
            lambda s: s['resources'][2]['attachments'][0].update(mac='changed'),
        ):
            broken = copy.deepcopy(after)
            mutate(broken)
            with self.assertRaises(ValueError):
                verify_states(before, broken)

    def test_each_guest_has_its_own_nat_observation(self):
        observations = {name: ['10.77.0.20'] * 2 for name in ('docker', 'firecracker', 'libvirt')}
        verify_translated_sources(observations, '10.77.0.20')
        for broken in ({'docker': ['10.77.0.20']},
                       dict(observations, firecracker=['10.49.0.2']),
                       dict(observations, libvirt=[])):
            with self.assertRaises(ValueError):
                verify_translated_sources(broken, '10.77.0.20')

class ReleaseArtifactTests(unittest.TestCase):
    def test_release_selection_rejects_changed_helper(self):
        import tempfile
        import json
        import hashlib
        from pathlib import Path
        from mixed_lifecycle import release_inputs
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root/'sysbox').write_bytes(b'cli')
            (root/'sysbox-init').write_bytes(b'helper')
            target = dict(architecture='amd64', sysbox_sha256=hashlib.sha256(b'cli').hexdigest(),
                          sysbox_init_sha256=hashlib.sha256(b'helper').hexdigest())
            (root/'build-metadata.json').write_text(json.dumps(dict(commit='candidate',targets=[target])))
            binary, helper, metadata = release_inputs(root/'build-metadata.json', root/'sysbox', root/'sysbox-init')
            self.assertEqual('candidate',metadata['commit'])
            (root/'sysbox-init').write_bytes(b'changed')
            with self.assertRaises(ValueError):
                release_inputs(root/'build-metadata.json', binary, helper)
