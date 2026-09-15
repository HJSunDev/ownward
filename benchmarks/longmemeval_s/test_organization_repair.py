import copy
import unittest
from unittest import mock
import tempfile
from pathlib import Path

import run
import organization_repair as repair
from external_intelligence import ExternalIntelligenceError, validate_structured_output


class LocationRepairTests(unittest.TestCase):
    def setUp(self):
        self.work = [{'id':'work-a','asset':{'id':'a','revision':1,'content':'Alice wrote the plan. Original context.'},
                      'candidates':[{'id':'b','revision':2,'content':'Bob reviews it.'},
                                    {'id':'c','revision':1,'content':'Unrelated retained candidate.'}]}]
        self.analysis = {'summary':'Original summary','topics':['plan'],'cues':[{'text':'plan','kind':'fact'}],
            'organization':{'schema':'ownward.organization/v2','units':[
                {'id':'u','selector':{'exact':'the plan'},'context':[{'exact':'Original context.'}],
                 'mentions':[{'id':'alice','name':'Alice','role':'author','selector':{'exact':'Alice'}}]},
                {'id':'other','selector':{'exact':'Original context.'}}],
                'links':[{'type':'related_to','meaning':'Original meaning',
                          'source':{'asset_id':'a','unit_id':'u'},'target':{'asset_id':'b','selector':{'exact':'Bob reviews it.'}}}]}}
        self.fb=[{'work_id':'work-a','error':'units[0] ("u"): 单元 "u" 的对象提及 "alice" 不在该单元或必要上下文内',
                  'rejected_analysis':self.analysis,'input_assets':[{'id':'a','revision':1},{'id':'b','revision':2},{'id':'c','revision':1}]}]

    def output(self, edits):
        return {'repairs':[{'work_id':'work-a','corrections':edits}]}

    def test_complete_own_source_and_protected_payload(self):
        prompt,schema=repair.request(self.work,self.fb,None)
        self.assertIn(self.work[0]['asset']['content'],prompt)
        self.assertNotIn('Unrelated retained candidate.',prompt)
        edits={'/units/0/context':[{'exact':'Original context.'},{'exact':'Alice wrote the plan.'}]}
        validate_structured_output(self.output(edits),schema)
        before=copy.deepcopy(self.analysis)
        accepted,errors=repair.apply(self.work,self.fb,self.output(edits))
        self.assertFalse(errors)
        expected=copy.deepcopy(self.analysis);expected['organization']['units'][0]['context']=edits['/units/0/context']
        self.assertEqual(accepted['work-a'],{**expected,'work_id':'work-a','input_assets':self.fb[0]['input_assets']})
        self.assertEqual(self.analysis,before)

    def test_foreign_endpoint_includes_original_foreign_material(self):
        self.fb[0]['error']='links[0].target: 单元 "foreign" 未声明对象提及 "m"'
        self.analysis['organization']['links'][0]['target']={'asset_id':'b','unit_id':'foreign','mention_id':'m'}
        prompt,schema=repair.request(self.work,self.fb,None)
        self.assertIn('Bob reviews it.',prompt)
        self.assertNotIn('Unrelated retained candidate.',prompt)
        bad=self.output({'/links/0/target':{'asset_id':'c','selector':{'exact':'Unrelated retained candidate.'}}})
        with self.assertRaises(ExternalIntelligenceError):validate_structured_output(bad,schema)

    def test_unknown_or_mixed_failures_use_broader_path(self):
        for error in ('stale snapshot','links[0] (same_object): source 与 target 指向同一端点',self.fb[0]['error']+'\nnew unknown error'):
            with self.subTest(error=error):
                self.fb[0]['error']=error
                with self.assertRaises(ExternalIntelligenceError):repair.request(self.work,self.fb,None)

    def test_protected_fields_and_evidence_cannot_be_removed(self):
        bad_edits=[{'/links':[]},{'/units/1/context':[]},{'/units/0/selector':{'exact':'Alice'}},
                   {'/units/0/context':[]},{'/units/0/mentions':[]},
                   {'/units/0/mentions':[{'id':'alice','name':'Bob','role':'author'}]},
                   {'/units/0/mentions':[{'id':'alice','name':'Alice','role':'reviewer'}]}]
        for edits in bad_edits:
            with self.subTest(edits=edits):
                accepted,errors=repair.apply(self.work,self.fb,self.output(edits))
                self.assertFalse(accepted);self.assertTrue(errors)

    def test_no_correction_or_reordered_work_is_not_accepted(self):
        accepted,errors=repair.apply(self.work,self.fb,self.output({}))
        self.assertFalse(accepted);self.assertTrue(errors)
        with self.assertRaises(ExternalIntelligenceError):repair.apply(self.work,self.fb,{'repairs':[]})

    def test_missing_referenced_source_cannot_reduce_evidence(self):
        self.fb[0]['error']='links[0].target: 单元 "foreign" 未声明对象提及 "m"'
        self.work[0]['candidates']=[]
        with self.assertRaises(ExternalIntelligenceError):repair.request(self.work,self.fb,None)

    def test_partial_batch_keeps_complete_correction(self):
        work=copy.deepcopy(self.work[0]);work['id']='work-second'
        fb=copy.deepcopy(self.fb[0]);fb['work_id']='work-second'
        value=self.output({'/units/0/context':[{'exact':'Original context.'},{'exact':'Alice wrote the plan.'}]})
        accepted,errors=repair.apply([self.work[0],work],[self.fb[0],fb],value,partial=True)
        self.assertEqual(list(accepted),['work-a'])
        self.assertEqual([x['work_id'] for x in errors],['work-second'])

    def test_conflicting_source_versions_use_full_repair(self):
        work=copy.deepcopy(self.work[0]);work['id']='work-second';work['asset']['revision']=2
        fb=copy.deepcopy(self.fb[0]);fb['work_id']='work-second'
        with self.assertRaises(ExternalIntelligenceError):repair.request([self.work[0],work],[self.fb[0],fb],None)

    def test_formal_capability_dispatch_and_broader_fallback(self):
        import run
        self.work[0]['organization_schema']='ownward.organization/v2'
        settings={**run.load_json(Path(run.__file__).with_name('protocol.json'))['memory'],'semantic_attempts':1}
        cap=run.ExternalIntelligenceCapability(mock.Mock())
        value=self.output({'/units/0/context':[{'exact':'Original context.'},{'exact':'Alice wrote the plan.'}]})
        with tempfile.TemporaryDirectory() as tmp:
            cap._invoke=mock.Mock(return_value=(value,{'calls':1}))
            result,usage=cap.semantics(self.work,settings,Path(tmp)/'locations',feedback=self.fb)
            self.assertEqual(result[0]['summary'],self.analysis['summary'])
            self.assertEqual(usage['calls'],1)
            self.assertIn('corrections',str(cap._invoke.call_args.kwargs['schema']))
            self.fb[0]['error']='unknown semantic failure'
            self.fb[0]['rejected_organization']=self.analysis['organization']
            normal={'analyses':[{'work_id':'work-a',**self.analysis}]}
            cap._invoke=mock.Mock(return_value=(normal,{'calls':1}))
            result,usage=cap.semantics(self.work,settings,Path(tmp)/'full',feedback=self.fb)
            self.assertEqual(cap._invoke.call_count,1)
            self.assertIn('analyses',cap._invoke.call_args.kwargs['schema']['properties'])
            self.assertNotIn('rejected_analysis',cap._invoke.call_args.kwargs['prompt'])

    def test_semantic_reuse_identity_includes_location_repair(self):
        import run
        before=run.semantic_implementation_identity()
        real=run.sha256
        with mock.patch.object(run,'sha256',side_effect=lambda path: 'changed' if Path(path).name=='organization_repair.py' else real(path)):
            self.assertNotEqual(before,run.semantic_implementation_identity())

    def test_failed_local_repair_uses_remaining_budget_for_full_repair(self):
        from types import SimpleNamespace
        submission={'work_id':'work-a','asset_id':'a','analysis':self.analysis,'input_assets':self.fb[0]['input_assets']}
        frozen={'question_identity':'q','batch_index':0,'batch_id':'batch','work_sha256':'work',
                'asset_ids':['a'],'work':self.work}
        analysis={k:frozen[k] for k in ('question_identity','batch_index','batch_id','work_sha256')}
        analysis.update(identity='analysis',submissions=[submission],usage=run._empty_usage(),work_ids=['work-a'])
        client=mock.Mock()
        client.call_tool.side_effect=[{'results':[{'error':self.fb[0]['error']}]},
                                      {'results':[{'error':'broader semantic correction needed'}]}, {'results':[{}]}]
        cap=mock.Mock()
        cap.semantics.return_value=([{'work_id':'work-a',**self.analysis,'input_assets':self.fb[0]['input_assets']}],{'calls':1})
        with tempfile.TemporaryDirectory() as tmp:
            result=run.submit_semantic_batch(SimpleNamespace(client=client),frozen,analysis,Path(tmp),cap,{'semantic_attempts':3})
            self.assertEqual(cap.semantics.call_count,2)
            self.assertIn('rejected_analysis',cap.semantics.call_args_list[0].kwargs['feedback'][0])
            self.assertNotIn('rejected_analysis',cap.semantics.call_args_list[1].kwargs['feedback'][0])
            self.assertEqual(result['usage']['calls'],2)
            self.assertEqual(client.call_tool.call_count,3)


if __name__=='__main__':unittest.main()
