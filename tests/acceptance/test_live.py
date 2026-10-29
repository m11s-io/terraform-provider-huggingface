import argparse
import os
import sys
import unittest
from unittest import mock
import live

class LiveSafetyTests(unittest.TestCase):
    def test_hcl_literals_preserve_git_branch_template_characters(self):
        args=argparse.Namespace(namespace='alice',github_repository='example/repo',branch='release-${1+1}',workflow='publish.yml')
        text=live.live_config(args,'alice/terraform-provider-acceptance-0123456789ab')
        self.assertIn('branch = "release-$${1+1}"',text)

    def test_hcl_literal_escape_applies_to_every_injected_field(self):
        args=argparse.Namespace(namespace='alice',github_repository='example/${repo}',branch='release-%{value}',workflow='${file}.yml')
        text=live.live_config(args,'alice/terraform-provider-acceptance-0123456789ab')
        self.assertIn('"example/$${repo}"',text)
        self.assertIn('"release-%%{value}"',text)
        self.assertIn('"$${file}.yml"',text)

    def test_purge_rejects_production_and_other_namespace(self):
        for repo in ['alice/Timestral-5M-OpenVINO-experimental','other/terraform-provider-acceptance-0123456789ab']:
            argv=['live.py','--mode','purge','--confirm-disposable','--namespace','alice','--github-repository','example/repo','--branch','main','--repo-id',repo,'--scope-repo-id','alice/terraform-provider-acceptance-0123456789ab-scope']
            with mock.patch.object(sys,'argv',argv),mock.patch.object(live,'api') as api:
                with self.assertRaises(SystemExit):live.main()
                api.assert_not_called()
    def test_upload_refuses_bootstrap_credential_before_sdk_or_network(self):
        with mock.patch.dict(os.environ,{'HF_TOKEN':'fake-bootstrap'}),mock.patch.object(live,'api') as api:
            with self.assertRaisesRegex(RuntimeError,'bootstrap'):live.oidc_upload({})
            api.assert_not_called()
    def test_setup_failure_cleans_both_unique_names(self):
        args=argparse.Namespace(namespace='alice')
        with mock.patch.dict(os.environ,{'GITHUB_OUTPUT':''}),mock.patch.object(live,'cli_runner',side_effect=RuntimeError('failure')),mock.patch.object(live,'delete_repo') as delete:
            with self.assertRaises(RuntimeError):live.setup(args,'fake')
            targets=[call.args[0] for call in delete.call_args_list]
            self.assertEqual(len(targets),2)
            self.assertTrue(targets[0].startswith('alice/terraform-provider-acceptance-'))
            self.assertEqual(targets[0],targets[1]+'-scope')
    def test_cleanup_dependency_failure_still_deletes_both_parents(self):
        targets={'repo_id':'alice/terraform-provider-acceptance-0123456789ab','scope_repo_id':'alice/terraform-provider-acceptance-0123456789ab-scope'}
        with mock.patch.dict(sys.modules,{'huggingface_hub':None}),mock.patch.object(live,'delete_repo') as delete:
            with self.assertRaisesRegex(RuntimeError,'verification failed'):live.cleanup(argparse.Namespace(),'fake',targets)
            self.assertEqual([c.args[0] for c in delete.call_args_list],[targets['scope_repo_id'],targets['repo_id']])
if __name__=='__main__':unittest.main()
