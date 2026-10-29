#!/usr/bin/env python3
"""Install a pinned CLI from its official checksum-verified distribution."""
import hashlib
import io
from pathlib import Path
import platform
import sys
import urllib.request
import zipfile

VERSIONS = {'terraform': '1.15.9', 'tofu': '1.13.2'}

def install(cli, destination):
    version = VERSIONS[cli]
    system = platform.system().lower()
    arch = {'x86_64': 'amd64', 'arm64': 'arm64', 'aarch64': 'arm64'}[platform.machine().lower()]
    if system not in ('linux', 'darwin', 'windows'):
        raise SystemExit('Unsupported platform')
    base = (f'https://releases.hashicorp.com/terraform/{version}' if cli == 'terraform'
            else f'https://github.com/opentofu/opentofu/releases/download/v{version}')
    archive = f'{cli}_{version}_{system}_{arch}.zip'
    with urllib.request.urlopen(f'{base}/{cli}_{version}_SHA256SUMS', timeout=60) as r:
        sums = r.read(1024 * 1024).decode()
    expected = next(line.split()[0] for line in sums.splitlines() if line.split()[-1].lstrip('*') == archive)
    with urllib.request.urlopen(f'{base}/{archive}', timeout=120) as r:
        blob = r.read(256 * 1024 * 1024)
    if hashlib.sha256(blob).hexdigest() != expected:
        raise SystemExit('CLI checksum mismatch')
    name = cli + ('.exe' if system == 'windows' else '')
    target = Path(destination) / name
    target.parent.mkdir(parents=True, exist_ok=True)
    with zipfile.ZipFile(io.BytesIO(blob)) as bundle:
        target.write_bytes(bundle.read(name))
    target.chmod(0o755)
    print(target.resolve())

if __name__ == '__main__':
    if len(sys.argv) != 3 or sys.argv[1] not in VERSIONS:
        raise SystemExit('Usage: install_cli.py terraform|tofu destination-directory')
    install(sys.argv[1], sys.argv[2])
