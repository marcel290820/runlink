#!/usr/bin/env python3
"""Validate and render infrastructure without an account or cloud API."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

root = Path(__file__).resolve().parents[1]
infra = root/'infra'

def run(command, **kwargs):
    return subprocess.run(command, check=True, cwd=root, **kwargs)

run(['tofu', '-chdir=infra', 'fmt', '-check', '-recursive'])
run(['tofu', '-chdir=infra', 'init', '-backend=false', '-input=false', '-lockfile=readonly'])
run(['tofu', '-chdir=infra', 'validate'])
result = run(['tofu', '-chdir=infra', 'test', '-json', '-verbose'], capture_output=True, text=True)
messages = [json.loads(line) for line in result.stdout.splitlines()]
summary = next(message['test_summary'] for message in messages if message['type']=='test_summary')
assert summary['failed'] == 0 and summary['passed'] >= 2
state = next(message['test_state'] for message in messages if message['type']=='test_state')
servers = [resource for resource in state['values']['root_module']['resources'] if resource['type']=='hcloud_server']
assert len(servers) == 3 and {server['index'] for server in servers} == {'a','b','c'}
rendered = root/'build/infra'
rendered.mkdir(parents=True, exist_ok=True)
for server in servers:
    role = server['index']
    cloud = json.loads(server['values']['user_data'].split('\n',1)[1])
    assert cloud['package_update'] and cloud['package_upgrade']
    assert cloud['runcmd'] == [['bash','/usr/local/lib/runlink/initialize.sh']]
    fixture = rendered/role
    # These are reserved generated fixtures; regenerate so removed templates cannot survive.
    if fixture.exists(): shutil.rmtree(fixture)
    fixture.mkdir()
    (fixture/'cloud-init.json').write_text(server['values']['user_data'])
    for file in cloud['write_files']:
        dest = fixture/file['path'].lstrip('/')
        dest.parent.mkdir(parents=True, exist_ok=True)
        dest.write_text(file['content'])
        dest.chmod(int(file['permissions'],8))
        if dest.suffix=='.sh' or file['path'].endswith('/runlink-tls'):
            run(['bash','-n',str(dest)])
            run(['shellcheck',str(dest)])
    (fixture/'cloud-init-schema.json').write_text(json.dumps(cloud))
    # All three generated cloud-init documents are valid JSON-compatible YAML.
    if shutil.which('cloud-init'):
        # Call the pure schema validator, avoiding host datasource/state discovery.
        run(['/usr/bin/python3', '-c',
             'import json,sys; from cloudinit.config.schema import validate_cloudconfig_schema; '
             'validate_cloudconfig_schema(json.load(open(sys.argv[1])), strict=True)',
             str(fixture/'cloud-init-schema.json')])
    else:
        print('cloud-init schema tool unavailable; checked structured content (run schema on Ubuntu before apply)')
    nft = shutil.which('nft')
    if nft:
        result = subprocess.run([nft,'--check','--file',str(fixture/'etc/nftables.conf')],capture_output=True,text=True)
        if result.returncode:
            if 'Operation not permitted' not in result.stderr or 'syntax error' in result.stderr:
                raise RuntimeError('nftables validation failed: '+result.stderr)
            print(f'{role}: nftables kernel validation requires CAP_NET_ADMIN on a disposable VM')
    else:
        print(f'{role}: nft binary unavailable; kernel validation remains on a disposable VM')

if shutil.which('systemd-analyze'):
    # Verify exact units in a temporary root; provide files and standard OS targets only.
    with tempfile.TemporaryDirectory() as temp:
        fixture = Path(temp)
        units = fixture/'etc/systemd/system'
        units.mkdir(parents=True)
        for role in ['a','b','c']:
            for unit in (rendered/role/'etc/systemd/system').glob('*.service'):
                shutil.copyfile(unit,units/unit.name)
        for target in ['basic','sysinit','sockets','shutdown','network-online','multi-user']:
            (units/f'{target}.target').write_text('[Unit]\nDescription=Fixture OS target\n')
        (units/'nftables.service').write_text('[Service]\nExecStart=/usr/bin/true\n')
        # Exact absolute ExecStart paths are populated, never executed by verify.
        for name in ['usr/bin/python3','usr/bin/turnserver','usr/bin/true','opt/runlink/current/bin/runlink-server']:
            dest=fixture/name
            dest.parent.mkdir(parents=True,exist_ok=True)
            dest.write_text('#!/bin/sh\nexit 0\n')
            dest.chmod(0o755)
        run(['systemd-analyze','verify','--man=no','--root',str(fixture),*[unit.name for unit in units.glob('*.service')]])
else:
    print('systemd-analyze unavailable; exact unit verification remains on Linux')
print('Infrastructure: provider validation, mocked stack, rendered cloud-init/shell/unit checks passed')
