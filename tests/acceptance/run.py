#!/usr/bin/env python3
"""Protocol lifecycle tests against a stateful loopback API, never HF."""
import argparse
import socket
import json
import os
from pathlib import Path
import subprocess
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

ROOT = Path(__file__).resolve().parents[2]

class Hub:
    def __init__(self):
        self.models = {}
        self.publishers = {}
        self.calls = []
        self.role = 'write'
        self.denied = False
        self.fail_write = False
        self.serial = 0
        self.lost_publisher_create = False
    def handler(self):
        hub = self
        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args): pass
            def reply(self, status, value):
                raw = value.encode() if isinstance(value, str) else json.dumps(value).encode()
                self.send_response(status)
                self.send_header('Content-Type', 'text/plain' if isinstance(value, str) else 'application/json')
                self.send_header('Content-Length', str(len(raw)))
                self.end_headers(); self.wfile.write(raw)
            def handle_request(self):
                path = self.path.split('?')[0]
                body = json.loads(self.rfile.read(int(self.headers.get('Content-Length', 0))) or '{}')
                hub.calls.append((self.command, path, body))
                if self.headers.get('Authorization') != 'Bearer acceptance-fake': return self.reply(401, {'error': 'unauthorized'})
                if path == '/api/whoami-v2':
                    return self.reply(200, {'type':'user','name':'alice','auth':{'type':'access_token','accessToken':{'role':hub.role}}})
                if self.command == 'POST' and path == '/api/repos/create':
                    key = body['organization']+'/'+body['name']
                    if key in hub.models: return self.reply(409, {'error':'exists'})
                    hub.models[key] = {'id':key,'private':body['private'],'sha':'ci-artifact-1','siblings':[{'rfilename':'weights.bin'}]}
                    return self.reply(200, {'id':'123456789012345678901234','name':key,'url':f'http://127.0.0.1:{self.server.server_port}/{key}','filesCopyPending':False})
                if self.command == 'DELETE' and path == '/api/repos/delete':
                    if hub.fail_write: return self.reply(403, {'error':'forbidden'})
                    key = body['organization']+'/'+body['name']
                    if key not in hub.models: return self.reply(404, {'error':'missing'})
                    del hub.models[key];hub.publishers.pop(key,None);return self.reply(200, 'OK')
                if path.startswith('/api/models/'):
                    parts=path[len('/api/models/'):].split('/')
                    key='/'.join(parts[:2]);tail='/'.join(parts[2:])
                    if hub.denied or key not in hub.models: return self.reply(404, {'error':'missing'})
                    if not tail and self.command=='GET': return self.reply(200,hub.models[key])
                    if tail=='settings' and self.command=='PUT':
                        if hub.fail_write:return self.reply(403,{'error':'forbidden'})
                        assert set(body)=={'private'},body
                        hub.models[key]['private']=body['private'];return self.reply(200,body)
                    if tail.startswith('settings/trusted-publishers'):
                        bindings=hub.publishers.setdefault(key,{})
                        identifier=tail.split('/')[2] if len(tail.split('/'))==3 else None
                        if self.command=='GET' and identifier is None:return self.reply(200,list(bindings.values()))
                        if hub.fail_write:return self.reply(403,{'error':'forbidden'})
                        if self.command=='POST' and identifier is None:
                            hub.serial+=1;identifier=f'{hub.serial:024x}';value={'_id':identifier,**body};bindings[identifier]=value
                            if hub.lost_publisher_create:
                                self.connection.shutdown(socket.SHUT_RDWR);self.connection.close();return
                            return self.reply(200,value)
                        if self.command=='DELETE' and identifier:
                            if identifier not in bindings:return self.reply(404,{'error':'Publisher not found'})
                            del bindings[identifier];return self.reply(200,'OK')
                self.reply(404,{'error':'route not found'})
            do_GET=do_POST=do_PUT=do_DELETE=handle_request
        return Handler

def config(name='model', private=True, policy='retain', publisher=False):
    text='''terraform {
  required_providers {
    huggingface = { source = "registry.terraform.io/m11s-io/huggingface" }
  }
}
provider "huggingface" {}
resource "huggingface_repository" "test" {
  namespace = "alice"
  name = %s
  private = %s
  deletion_policy = %s
}
''' % (json.dumps(name), str(private).lower(), json.dumps(policy))
    if publisher:
        text+='''resource "huggingface_trusted_publisher" "test" {
  repo_id = huggingface_repository.test.repo_id
  github_repository = "example/training"
  branch = "main"
  workflow = "publish.yml"
}
'''
    return text

class Runner:
    def __init__(self, cli, work, env): self.cli,self.work,self.env=cli,work,env
    def run(self,*args,expected=0):
        p=subprocess.run([self.cli,*args],cwd=self.work,env=self.env,text=True,stdout=subprocess.PIPE,stderr=subprocess.STDOUT)
        if p.returncode!=expected:raise AssertionError(f'{self.cli} {args} exit {p.returncode}, expected {expected}\n{p.stdout}')
        return p.stdout
    def apply(self):return self.run('apply','-auto-approve','-input=false','-no-color')
    def plan(self,code=0):return self.run('plan','-input=false','-no-color','-detailed-exitcode',expected=code)
    def state(self):return json.loads(self.run('show','-json'))['values']['root_module']['resources']

def repository_scenario(r,hub,work):
    cfg=work/'main.tf';cfg.write_text(config())
    assert not hub.calls
    r.run('plan','-out=initial.plan','-input=false','-no-color')
    initial=json.loads(r.run('show','-json','initial.plan'))
    change=initial['resource_changes'][0]
    assert change['change']['after_unknown']['repo_id'] is True
    assert not hub.calls, 'planning a new identity made early API calls'
    r.apply();r.plan();assert hub.models['alice/model']['private'] is True
    hub.models['alice/model']['sha']='ci-artifact-2';r.plan()
    cfg.write_text(config(private=False));r.apply();assert hub.models['alice/model']['private'] is False
    assert hub.models['alice/model']['sha']=='ci-artifact-2'
    writes=lambda:len([c for c in hub.calls if c[0]=='PUT'])
    before=writes();cfg.write_text(config(private=False,policy='delete'));r.apply();assert writes()==before
    r.run('state','rm','huggingface_repository.test')
    before=len([c for c in hub.calls if c[0]!='GET'])
    r.run('import','-input=false','-no-color','huggingface_repository.test','model/alice/model')
    assert len([c for c in hub.calls if c[0]!='GET'])==before
    assert r.state()[0]['values']['deletion_policy']=='retain'
    cfg.write_text(config(name='replacement',private=False));r.run('plan','-out=replace.plan','-input=false','-no-color')
    changes=json.loads(r.run('show','-json','replace.plan'))['resource_changes']
    assert changes[0]['change']['actions']==['delete','create']
    r.apply();assert 'alice/model' in hub.models and 'alice/replacement' in hub.models
    before=len(hub.calls);r.run('destroy','-auto-approve','-input=false','-no-color')
    assert 'alice/replacement' in hub.models
    assert not any(c[0]=='DELETE' for c in hub.calls[before:])
    cfg.write_text(config(name='delete-me',policy='delete'));r.apply()
    hub.denied=True;hub.role='read';state_before=(work/'terraform.tfstate').read_bytes();r.plan(1)
    assert (work/'terraform.tfstate').read_bytes()==state_before
    hub.denied=False;hub.role='write';hub.fail_write=True
    cfg.write_text(config(name='delete-me',private=False,policy='delete'));r.run('apply','-auto-approve','-input=false','-no-color',expected=1)
    assert r.state()[0]['values']['private'] is True
    hub.fail_write=False;r.apply();r.run('destroy','-auto-approve','-input=false','-no-color');assert 'alice/delete-me' not in hub.models
    cfg.write_text(config(name='removed'));r.apply();del hub.models['alice/removed'];r.plan(2)

def publisher_scenario(r,hub,work):
    r.run('destroy','-auto-approve','-input=false','-no-color')
    name='trust-model';cfg=work/'main.tf';cfg.write_text(config(name=name,publisher=True));r.apply();r.plan()
    key='alice/'+name;bindings=hub.publishers[key]
    original=next(iter(bindings))
    for private,policy in [(False,'retain'),(False,'delete'),(True,'retain')]:
        cfg.write_text(config(name=name,private=private,policy=policy,publisher=True))
        r.run('plan','-out=identity-stability.plan','-input=false','-no-color')
        changes=json.loads(r.run('show','-json','identity-stability.plan'))['resource_changes']
        trust=next(v for v in changes if v['address']=='huggingface_trusted_publisher.test')
        assert trust['change']['actions']==['no-op'],trust['change']['actions']
        before=len([c for c in hub.calls if c[0] in ('POST','DELETE') and 'trusted-publishers' in c[1]])
        r.apply()
        assert set(bindings)=={original}
        assert len([c for c in hub.calls if c[0] in ('POST','DELETE') and 'trusted-publishers' in c[1]])==before
    managed=next(iter(bindings));foreign='ffffffffffffffffffffffff'
    bindings[foreign]={'_id':foreign,'issuerUrl':'https://foreign.example','claims':{'unmanaged':{'kind':'eq','value':7}}}
    bindings[managed]['lastUsedAt']='2030-01-01T00:00:00Z';r.plan()
    previous_bindings=bindings
    name='trust-replacement';cfg.write_text(config(name=name,publisher=True))
    r.run('plan','-out=actual-replacement.plan','-input=false','-no-color')
    changes=json.loads(r.run('show','-json','actual-replacement.plan'))['resource_changes']
    for change in changes:assert change['change']['actions']==['delete','create'],change
    r.apply()
    assert set(previous_bindings)=={foreign} and key in hub.models
    key='alice/'+name;bindings=hub.publishers[key];managed=next(iter(bindings))
    bindings[foreign]={'_id':foreign,'issuerUrl':'https://foreign.example','claims':{'unmanaged':{'kind':'eq','value':7}}}
    r.plan()
    bindings[managed]['claims']['ref']['value']='refs/heads/external-drift'
    r.plan(2);before=len(hub.calls);r.apply()
    mutations=[c[0] for c in hub.calls[before:] if 'trusted-publishers' in c[1] and c[0]!='GET']
    assert mutations==['DELETE','POST'],mutations
    assert foreign in bindings
    managed=next(v for v in bindings if v!=foreign)
    r.run('state','rm','huggingface_trusted_publisher.test')
    before=len([c for c in hub.calls if c[0]!='GET'])
    r.run('import','-input=false','-no-color','huggingface_trusted_publisher.test','model/'+key+'/'+managed)
    assert len([c for c in hub.calls if c[0]!='GET'])==before
    r.plan()
    hub.denied=True;hub.role='read';before=(work/'terraform.tfstate').read_bytes();r.plan(1)
    assert (work/'terraform.tfstate').read_bytes()==before
    hub.denied=False;hub.role='write';hub.fail_write=True
    cfg.write_text(config(name=name,publisher=True).replace('branch = "main"','branch = "release"'))
    r.run('apply','-auto-approve','-input=false','-no-color',expected=1)
    assert managed in bindings
    hub.fail_write=False;cfg.write_text(config(name=name,publisher=True))
    r.plan();del bindings[managed];r.plan(2);r.apply()
    managed=next(v for v in bindings if v!=foreign)
    r.run('state','rm','huggingface_trusted_publisher.test');del bindings[managed]
    hub.lost_publisher_create=True;before=len([c for c in hub.calls if c[0]=='POST' and 'trusted-publishers' in c[1]])
    first=r.run('apply','-auto-approve','-input=false','-no-color',expected=1)
    diagnostic=' '.join(first.lower().split())
    assert 'import' in diagnostic and 'may have succeeded' in diagnostic,first
    r.run('apply','-auto-approve','-input=false','-no-color',expected=1)
    assert len([c for c in hub.calls if c[0]=='POST' and 'trusted-publishers' in c[1]])==before+1
    hub.lost_publisher_create=False;managed=next(v for v in bindings if v!=foreign)
    r.run('import','-input=false','-no-color','huggingface_trusted_publisher.test','model/'+key+'/'+managed)
    r.plan();r.run('destroy','-auto-approve','-input=false','-no-color')
    assert set(bindings)=={foreign} and key in hub.models
    print(f'PASS {r.cli}: publisher graph/import/drift/destroy-before-create/foreign binding/lost-response no duplicate',flush=True)


def main():
    parser=argparse.ArgumentParser();parser.add_argument('--cli',action='append',required=True);parser.add_argument('--publisher',action='store_true');args=parser.parse_args()
    with tempfile.TemporaryDirectory(prefix='hf-protocol-') as temporary:
        root=Path(temporary);binary=root/'bin';binary.mkdir()
        subprocess.run(['go','build','-o',str(binary/'terraform-provider-huggingface'),'.'],cwd=ROOT,check=True)
        cli_config=root/'cli.tfrc';cli_config.write_text('provider_installation {\n dev_overrides { "registry.terraform.io/m11s-io/huggingface" = '+json.dumps(str(binary))+' }\n direct {}\n}\n')
        for index,cli in enumerate(args.cli):
            hub=Hub();server=ThreadingHTTPServer(('127.0.0.1',0),hub.handler());thread=threading.Thread(target=server.serve_forever,daemon=True);thread.start()
            work=root/str(index);work.mkdir();env=os.environ.copy();env.update(HF_TOKEN='acceptance-fake',HF_ENDPOINT=f'http://127.0.0.1:{server.server_port}',TF_CLI_CONFIG_FILE=str(cli_config),TF_IN_AUTOMATION='1',CHECKPOINT_DISABLE='1')
            try:
                r=Runner(str(Path(cli).resolve()),work,env);repository_scenario(r,hub,work)
                if args.publisher:publisher_scenario(r,hub,work)
                print(f'PASS {cli}: repository lifecycle/import/no-op/drift/replacement/policies',flush=True)
            finally:server.shutdown();server.server_close();thread.join()
if __name__=='__main__':main()
