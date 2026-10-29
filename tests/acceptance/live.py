#!/usr/bin/env python3
"""Explicit opt-in live acceptance and manual real GitHub OIDC smoke test."""
import argparse
import io
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import urllib.error
import urllib.parse
import urllib.request
import uuid

from run import ROOT, Runner, config

MARKER='provider-smoke-marker.txt'
ENDPOINT='https://huggingface.co'
class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self,*args,**kwargs): return None

def api(method,path,body=None,token=None):
    headers={'Content-Type':'application/json'}
    if token:headers['Authorization']='Bearer '+token
    request=urllib.request.Request(ENDPOINT+path,method=method,headers=headers,data=None if body is None else json.dumps(body).encode())
    try:
        with urllib.request.build_opener(NoRedirect).open(request,timeout=30) as response:
            status,raw=response.status,response.read(2_000_000)
    except urllib.error.HTTPError as error:status,raw=error.code,error.read(2_000_000)
    try:value=json.loads(raw)
    except ValueError:value={}
    return status,value

def must(status,value,expected=200):
    if status!=expected:raise RuntimeError(f'Live API status {status}, expected {expected}')
    return value

def delete_repo(repo,token):
    namespace,name=repo.split('/')
    status,_=api('DELETE','/api/repos/delete',{'organization':namespace,'name':name,'type':'model'},token)
    if status not in (200,404):raise RuntimeError(f'Cleanup failed for {repo}, status {status}')
    status,_=api('GET','/api/models/'+repo,token=token)
    if status!=404:raise RuntimeError(f'Cleanup could not verify deletion of {repo}')

def publisher_body(repository,branch,workflow):
    return {'issuerUrl':'https://token.actions.githubusercontent.com','claims':{
        'repository':{'kind':'eq','value':repository},'ref':{'kind':'eq','value':'refs/heads/'+branch},
        'workflow_ref':{'kind':'prefix','value':repository+'/.github/workflows/'+workflow+'@'}}}

def cli_runner(args,temporary):
    binary=temporary/'bin';binary.mkdir()
    subprocess.run(['go','build','-o',str(binary/'terraform-provider-huggingface'),'.'],cwd=ROOT,check=True)
    rc=temporary/'cli.tfrc';rc.write_text('provider_installation {\n dev_overrides { "registry.terraform.io/m11s-io/huggingface" = '+json.dumps(str(binary))+' }\n direct {}\n}\n')
    work=temporary/'work';work.mkdir();env=os.environ.copy();env.update(TF_CLI_CONFIG_FILE=str(rc),HF_ENDPOINT=ENDPOINT,CHECKPOINT_DISABLE='1',TF_IN_AUTOMATION='1')
    return Runner(shutil.which(args.cli) or args.cli,work,env),work

def hcl_string(value):
    return json.dumps(value.replace("${", "$${").replace("%{", "%%{"))

def live_config(args,repo):
    return config(name=repo.split('/')[1],publisher=True).replace('namespace = "alice"','namespace = '+hcl_string(args.namespace)).replace('"example/training"',hcl_string(args.github_repository)).replace('branch = "main"','branch = '+hcl_string(args.branch)).replace('workflow = "publish.yml"','workflow = '+hcl_string(args.workflow))

def setup(args,token):
    name='terraform-provider-acceptance-'+uuid.uuid4().hex[:12];repo=args.namespace+'/'+name;scope=repo+'-scope';success=False
    if os.environ.get('GITHUB_OUTPUT'):
        with open(os.environ['GITHUB_OUTPUT'],'a') as out:out.write('repo_id='+repo+'\nscope_repo_id='+scope+'\n')
    try:
        with tempfile.TemporaryDirectory(prefix='hf-live-') as directory:
            runner,work=cli_runner(args,Path(directory));(work/'main.tf').write_text(live_config(args,repo));runner.apply();runner.plan()
            state={r['type']:r['values'] for r in runner.state()};pid=state['huggingface_trusted_publisher']['publisher_id']
            runner.run('state','rm','huggingface_trusted_publisher.test','huggingface_repository.test')
            runner.run('import','-input=false','-no-color','huggingface_repository.test','model/'+repo)
            runner.run('import','-input=false','-no-color','huggingface_trusted_publisher.test','model/'+repo+'/'+pid);runner.plan()
            foreign=must(*api('POST','/api/models/'+repo+'/settings/trusted-publishers',publisher_body(args.github_repository,'unrelated-'+uuid.uuid4().hex,'never-run.yml'),token))['_id']
            must(*api('POST','/api/repos/create',{'organization':args.namespace,'name':name+'-scope','type':'model','private':True},token))
            must(*api('POST','/api/models/'+scope+'/settings/trusted-publishers',publisher_body(args.github_repository,args.branch,'never-run-mismatched-workflow.yml'),token))
            result={'repo_id':repo,'publisher_id':pid,'foreign_id':foreign,'scope_repo_id':scope}
            if args.output:Path(args.output).write_text(json.dumps(result)+'\n')
            if os.environ.get('GITHUB_OUTPUT'):
                with open(os.environ['GITHUB_OUTPUT'],'a') as out:
                    for key,value in result.items():out.write(key+'='+value+'\n')
            success=True;return result
    finally:
        if not success:
            # Unique names were selected before apply, so clean up even if a
            # response was lost. No retry of create is performed.
            failures=[]
            for target in (scope,repo):
                try:delete_repo(target,token)
                except Exception:failures.append(target)
            if failures:print('Manual cleanup required: '+', '.join(failures))

def cleanup(args,token,result):
    problem=None
    try:
        from huggingface_hub import HfApi, hf_hub_download
        hub=HfApi(endpoint=ENDPOINT,token=token)
        with tempfile.TemporaryDirectory(prefix='hf-cleanup-') as directory:
            runner,work=cli_runner(args,Path(directory));(work/'main.tf').write_text(live_config(args,result['repo_id']))
            runner.run('import','-input=false','-no-color','huggingface_repository.test','model/'+result['repo_id'])
            runner.run('import','-input=false','-no-color','huggingface_trusted_publisher.test','model/'+result['repo_id']+'/'+result['publisher_id'])
            runner.run('destroy','-auto-approve','-input=false','-no-color')
            must(*api('GET','/api/models/'+result['repo_id'],token=token))
            bindings=must(*api('GET','/api/models/'+result['repo_id']+'/settings/trusted-publishers',token=token))
            if {v['_id'] for v in bindings}!={result['foreign_id']}:raise RuntimeError('Unrelated binding was not preserved')
            if args.require_marker:
                artifact=hf_hub_download(result['repo_id'],MARKER,token=token,endpoint=ENDPOINT,local_dir=Path(directory)/'artifact')
                if Path(artifact).read_bytes()!=b'OIDC marker\n':raise RuntimeError('Retained artifact content changed')
                if MARKER in hub.list_repo_files(result['scope_repo_id']):raise RuntimeError('Scoped token wrote to second repository')
    except Exception as error:problem=type(error).__name__
    finally:
        failures=[]
        for target in (result['scope_repo_id'],result['repo_id']):
            try:delete_repo(target,token)
            except Exception:failures.append(target)
        if failures:raise RuntimeError('Manual cleanup required: '+', '.join(failures))
    if problem:raise RuntimeError('Live retain/preservation verification failed: '+problem)
    print('PASS live import, retained artifact, unrelated publisher and cleanup')

def oidc_upload(result):
    if os.environ.get('HF_TOKEN'):raise RuntimeError('Upload job must not receive a bootstrap HF_TOKEN')
    from huggingface_hub import HfApi
    url=os.environ['ACTIONS_ID_TOKEN_REQUEST_URL']
    url+=('&' if '?' in url else '?')+urllib.parse.urlencode({'audience':ENDPOINT})
    request=urllib.request.Request(url,headers={'Authorization':'Bearer '+os.environ['ACTIONS_ID_TOKEN_REQUEST_TOKEN']})
    with urllib.request.urlopen(request,timeout=30) as response:jwt=json.load(response)['value']
    def exchange(resource):
        return api('POST','/oauth/token',{'grant_type':'urn:ietf:params:oauth:grant-type:token-exchange','subject_token_type':'urn:ietf:params:oauth:token-type:id_token','subject_token':jwt,'resource':resource})
    status,value=exchange(result['repo_id']);must(status,value);access=value['access_token']
    status,value=exchange(result['scope_repo_id'])
    if status!=400 or value.get('error')!='invalid_grant':raise RuntimeError('Mismatched workflow claims were not rejected')
    hub=HfApi(endpoint=ENDPOINT,token=access)
    hub.upload_file(path_or_fileobj=io.BytesIO(b'OIDC marker\n'),path_in_repo=MARKER,repo_id=result['repo_id'])
    try:hub.upload_file(path_or_fileobj=io.BytesIO(b'must not write\n'),path_in_repo=MARKER,repo_id=result['scope_repo_id'])
    except Exception as error:
        if getattr(getattr(error,'response',None),'status_code',None) not in (401,403,404):raise RuntimeError('Scope rejection not established') from None
    else:raise RuntimeError('Repo-scoped token wrote to another repository')
    print('PASS real GitHub OIDC upload, mismatched workflow rejection and token scope')

def main():
    p=argparse.ArgumentParser();p.add_argument('--mode',choices=['setup','upload','cleanup','purge','roundtrip'],required=True);p.add_argument('--namespace',required=True);p.add_argument('--github-repository',required=True);p.add_argument('--branch',required=True);p.add_argument('--workflow',default='hf-acceptance.yml');p.add_argument('--cli',default='tofu');p.add_argument('--output');p.add_argument('--repo-id');p.add_argument('--scope-repo-id');p.add_argument('--publisher-id');p.add_argument('--foreign-id');p.add_argument('--require-marker',action='store_true');p.add_argument('--confirm-disposable',action='store_true',required=True);args=p.parse_args()
    if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._-]*',args.namespace):raise SystemExit('Invalid dedicated namespace')
    result={'repo_id':args.repo_id,'scope_repo_id':args.scope_repo_id,'publisher_id':args.publisher_id,'foreign_id':args.foreign_id}
    if args.mode in ('cleanup','upload','purge'):
        for key in ('repo_id','scope_repo_id'):
            if not result[key] or not re.fullmatch(re.escape(args.namespace)+r'/terraform-provider-acceptance-[a-f0-9]{12}(?:-scope)?',result[key]):raise SystemExit('Only uniquely named disposable test repositories are allowed')
    token=os.environ.get('HF_TOKEN')
    if args.mode!='upload' and not token:raise SystemExit('HF_TOKEN bootstrap credential required')
    try:
        if args.mode=='setup':setup(args,token)
        elif args.mode=='cleanup':cleanup(args,token,result)
        elif args.mode=='upload':oidc_upload(result)
        elif args.mode=='purge':
            for target in (result['scope_repo_id'],result['repo_id']):delete_repo(target,token)
        else:
            result=setup(args,token)
            try:
                from huggingface_hub import HfApi
                HfApi(endpoint=ENDPOINT,token=token).upload_file(path_or_fileobj=io.BytesIO(b'OIDC marker\n'),path_in_repo=MARKER,repo_id=result['repo_id'])
                args.require_marker=True
            finally:cleanup(args,token,result)
    except Exception as error:
        if args.mode in ('cleanup','purge','upload'):
            print('Disposable repositories to inspect: '+result['repo_id']+', '+result['scope_repo_id'])
        # Tokens/JWTs and raw remote exception bodies are never printed or saved.
        raise SystemExit('Live test failed ('+type(error).__name__+'); cleanup attempted. See any manual-cleanup identifiers above.') from None
if __name__=='__main__':main()
