import json
import tempfile
import unittest
from pathlib import Path
import campaign


def events(*tests):
    rows=[]
    for name in tests:
        rows.extend([dict(Action='run',Package='pkg',Test=name),dict(Action='pass',Package='pkg',Test=name)])
    rows.append(dict(Action='pass',Package='pkg'))
    return '\n'.join(json.dumps(r) for r in rows)


class CampaignTests(unittest.TestCase):
    def test_missing_skip_unfinished_or_failure_never_passes(self):
        valid=events('TestOne','TestTwo')
        campaign.verify_events(valid,['TestOne','TestTwo'])
        for raw in [events('TestOne'), valid.replace('"pass"','"skip"',1), valid+'\n'+json.dumps(dict(Action='fail',Package='pkg')), valid+'\n'+json.dumps(dict(Action='run',Package='pkg',Test='TestExtra')), 'not-json']:
            with self.subTest(raw=raw):
                with self.assertRaises(ValueError): campaign.verify_events(raw,['TestOne','TestTwo'])

    def test_catalog_covers_every_original_requirement_exactly(self):
        root=Path(__file__).resolve().parents[2]
        catalog=campaign.load_catalog(root/'scripts/acceptance/campaign.json',root)
        self.assertEqual(len(catalog['requirements']),65)
        self.assertEqual({r['id'].split('.')[0] for r in catalog['requirements']},
                         {f'P{i}' for i in range(10)})
        copied=json.loads(json.dumps(catalog));copied['requirements'][0]['text'] += ' changed'
        with self.assertRaises(ValueError): campaign.validate_catalog(copied,root)
        copied=json.loads(json.dumps(catalog));copied['requirements'].pop()
        with self.assertRaises(ValueError): campaign.validate_catalog(copied,root)
        copied=json.loads(json.dumps(catalog));copied['gates'][0]['tests']=['TestDoesNotExist']
        with self.assertRaises(ValueError): campaign.validate_catalog(copied,root)

    def test_archived_requirements_reject_corrupted_archive_or_source_digest(self):
        repository = Path(__file__).resolve().parents[2]
        original = repository / 'docs/archive'
        archive_name = '2026-10-27-documentation.tar.gz'
        manifest_name = '2026-10-27-documentation.json'
        source = 'docs/design/2026-10-07-full-migration-distributed-execution-plan.md'
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            target = root / 'docs/archive'
            target.mkdir(parents=True)
            raw = (original / archive_name).read_bytes()
            manifest = json.loads((original / manifest_name).read_text())
            (target / archive_name).write_bytes(raw)
            (target / manifest_name).write_text(json.dumps(manifest))
            self.assertEqual(campaign.original_requirements(root),
                             campaign.original_requirements(repository))
            (target / archive_name).write_bytes(raw + b'corrupted')
            with self.assertRaisesRegex(ValueError, 'archive digest mismatch'):
                campaign.original_requirements(root)
            (target / archive_name).write_bytes(raw)
            manifest['files'][source]['sha256'] = '0' * 64
            (target / manifest_name).write_text(json.dumps(manifest))
            with self.assertRaisesRegex(ValueError, 'source digest mismatch'):
                campaign.original_requirements(root)

    def test_evidence_requires_matching_source_and_untampered_raw_events(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);path=root/'gate.jsonl';path.write_text(events('TestOne').replace('\"pkg\"','\"github.com/oslab/sysbox/pkg/example\"'))
            gate={'id':'gate','tests':['TestOne'],'package':'./pkg/example'}
            result={'id':'gate','source_sha256':'source','exit_code':0,'events':'gate.jsonl','events_sha256':campaign.sha256(path.read_bytes()),'status':'passed','package':gate['package'],'tests':gate['tests'],'event_package':'github.com/oslab/sysbox/pkg/example','artifacts':[]}
            campaign.validate_evidence(root,result,gate,'source')
            with self.assertRaises(ValueError):campaign.validate_evidence(root,result,gate,'changed')
            result['exit_code']=1
            with self.assertRaises(ValueError):campaign.validate_evidence(root,result,gate,'source')
            result['exit_code']=0;path.write_text(events('TestDifferent'))
            with self.assertRaises(ValueError):campaign.validate_evidence(root,result,gate,'source')

    def test_contract_evidence_cannot_close_documented_scope_gaps(self):
        req={'id':'P9.3','gates':['contract'],'remaining':['full provider fault matrix']}
        self.assertEqual(campaign.requirement_status(req,{'contract':'passed'}),'open')
        req['remaining']=[]
        self.assertEqual(campaign.requirement_status(req,{'contract':'blocked'}),'unverified')
        self.assertEqual(campaign.requirement_status(req,{'contract':'passed'}),'verified')
        req['gates']=[]
        self.assertEqual(campaign.requirement_status(req,{}),'unverified')

class ArtifactEvidenceTests(unittest.TestCase):
    def test_external_artifact_changes_invalidate_evidence(self):
        with tempfile.TemporaryDirectory() as tmp:
            artifact=Path(tmp)/'helper';artifact.write_bytes(b'original')
            recorded=campaign.fingerprint_artifact(artifact)
            campaign.verify_artifacts([recorded])
            artifact.write_bytes(b'changed')
            with self.assertRaises(ValueError):campaign.verify_artifacts([recorded])

    def test_event_package_must_match_selected_package(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);path=root/'gate.jsonl';path.write_text(events('TestOne'))
            gate={'id':'gate','tests':['TestOne'],'package':'./pkg/example'}
            result={'id':'gate','source_sha256':'source','exit_code':0,'events':'gate.jsonl','events_sha256':campaign.sha256(path.read_bytes()),'status':'passed','package':gate['package'],'tests':gate['tests'],'event_package':'github.com/oslab/sysbox/pkg/example','artifacts':[]}
            with self.assertRaises(ValueError):campaign.validate_evidence(root,result,gate,'source')
