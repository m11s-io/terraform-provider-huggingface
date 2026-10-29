#!/usr/bin/env python3
"""Validate snapshot packaging in isolation; never publish or modify LICENSE."""
import argparse
import io
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile

ROOT=Path(__file__).resolve().parents[2]
def main():
    p=argparse.ArgumentParser();p.add_argument('--goreleaser',default='goreleaser');p.add_argument('--license-template',type=Path);args=p.parse_args()
    executable=shutil.which(args.goreleaser) or args.goreleaser
    version=subprocess.check_output([executable,'--version'],text=True)
    if '2.19.0' not in version:raise SystemExit('Use pinned GoReleaser 2.19.0')
    archive=subprocess.check_output(['git','archive','HEAD'],cwd=ROOT)
    with tempfile.TemporaryDirectory(prefix='hf-snapshot-') as temporary:
        stage=Path(temporary)
        with tarfile.open(fileobj=io.BytesIO(archive)) as bundle:bundle.extractall(stage,filter='data')
        if not (stage/'LICENSE').exists():
            if not args.license_template:raise SystemExit('Infrastructure-owned LICENSE absent. For local validation only, pass --license-template /path/to/infrastructure/MPL-template.')
            shutil.copyfile(args.license_template,stage/'LICENSE')
        # Snapshot only committed content. Isolated staging metadata avoids touching
        # the real branch or infrastructure-owned file during packaging validation.
        subprocess.run(['git','init','-q'],cwd=stage,check=True)
        subprocess.run(['git','add','.'],cwd=stage,check=True)
        subprocess.run(['git','-c','user.name=Snapshot validation','-c','user.email=snapshot@example.invalid','commit','-qm','Snapshot validation'],cwd=stage,check=True)
        subprocess.run([executable,'check'],cwd=stage,check=True)
        subprocess.run([executable,'release','--snapshot','--clean','--skip=publish,sign'],cwd=stage,check=True)
        archives=list((stage/'dist').glob('*.zip'))
        if len(archives)!=5:raise SystemExit(f'Expected five archives, got {len(archives)}')
        import zipfile
        for artifact in archives:
            with zipfile.ZipFile(artifact) as z:
                if 'LICENSE' not in z.namelist():raise SystemExit('Archive missing infrastructure license')
        sums=list((stage/'dist').glob('*_SHA256SUMS'))
        if len(sums)!=1:raise SystemExit('Missing checksums')
        import json,hashlib
        # GoReleaser checksums the renamed release extra-file in place; a
        # snapshot does not copy that manifest into dist. Validate the actual
        # named checksum against its source, not a nonexistent dist copy.
        manifest=stage/'terraform-registry-manifest.json'
        if json.loads(manifest.read_text())['metadata']['protocol_versions']!=['6.0']:raise SystemExit('Wrong registry protocol manifest')
        entries={line.split()[-1].lstrip('*'):line.split()[0] for line in sums[0].read_text().splitlines()}
        manifest_names=[name for name in entries if name.endswith('_manifest.json')]
        expected_name=sums[0].name.removesuffix('SHA256SUMS')+'manifest.json'
        if manifest_names!=[expected_name]:raise SystemExit('Missing or incorrectly named manifest checksum')
        if entries[expected_name]!=hashlib.sha256(manifest.read_bytes()).hexdigest():raise SystemExit('Registry manifest checksum mismatch')
        for artifact in archives:
            if entries.get(artifact.name)!=hashlib.sha256(artifact.read_bytes()).hexdigest():raise SystemExit('Checksum mismatch for '+artifact.name)
        print('PASS: five snapshot archives, checksums and registry manifest; publish/sign skipped')
if __name__=='__main__':main()
