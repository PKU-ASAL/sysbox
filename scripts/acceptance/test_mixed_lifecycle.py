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
