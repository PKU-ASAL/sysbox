import copy
import unittest
import fault_campaign

class FaultEvidenceTests(unittest.TestCase):
    def test_complete_requires_actual_same_task_fault_and_cleanup(self):
        summary={'phase':'complete','lab_id':'lab','topology':'p9-test','fault':'failover','metadata_deleted':True,'provider_residue':[],'owner_uid':'owner','binary_identity_kind':'service-proc-exe-v1','binary_sha256':{'control':'a'*64,'execution':'a'*64},'binary_sha256_after':{'control':'a'*64,'execution':'a'*64}}
        handle={'ID':'task'}
        fault={'lab_id':'lab','topology':'p9-test','primary_stopped':True,'primary_restored':True,'primary_still_stopped_at_completion':True,'before':{'handle':handle,'run_id':'run','provider_node_id':'container'},'after':{'handle':handle,'run_id':'run','provider_node_id':'container','reported':True,'stop_receipt':{'signature':'signed'},'result':{'ExitCodeKnown':True,'ExitCode':0,'RemoteTermination':'confirmed'}},'parent_on_backup':{'status':'done','host_completion':{'receipt':{}}}}
        cleanup={'topology':'p9-test','phase':'metadata_deleted','metadata_delete':{},'state_after_destroy':{'topology_uid':'owner','resources':[]}}
        fault_campaign.verify_round(summary,fault,cleanup)
        for change in ['restore','task','residue','cleanup','parent','missing-run','missing-provider','binary']:
            s,f,c=copy.deepcopy((summary,fault,cleanup))
            if change=='restore':f['primary_restored']=False
            if change=='task':f['after']['handle']={'ID':'other'}
            if change=='residue':s['provider_residue']=['remaining']
            if change=='cleanup':c['state_after_destroy']['resources']=[{}]
            if change=='parent':f['parent_on_backup']['status']='failed'
            if change=='missing-run':f['before'].pop('run_id');f['after'].pop('run_id')
            if change=='binary':s['binary_sha256_after']['execution']='b'*64
            if change=='missing-provider':f['before'].pop('provider_node_id');f['after'].pop('provider_node_id')
            with self.subTest(change=change),self.assertRaises(ValueError):fault_campaign.verify_round(s,f,c)
