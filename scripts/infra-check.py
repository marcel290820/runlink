#!/usr/bin/env python3
"""Validate the OpenTofu stack and its rendered machine configuration offline.

The mocked test run renders each role's cloud-init into build/infra/<role> for
review and for the coturn check. No cloud account or API is contacted.
"""
import json
from pathlib import Path
import shutil
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]
RENDERED = ROOT / 'build/infra'
ROLES = ('a', 'b', 'c')


def run(*command, **kwargs):
    return subprocess.run(command, check=True, cwd=ROOT, **kwargs)


def tofu(*args, **kwargs):
    return run('tofu', '-chdir=infra', *args, **kwargs)


def mocked_servers():
    """Run the mocked tests and return the three planned servers' values by role."""
    tofu('fmt', '-check', '-recursive')
    tofu('init', '-backend=false', '-input=false', '-lockfile=readonly')
    tofu('validate')
    output = tofu('test', '-json', '-verbose', capture_output=True, text=True).stdout
    messages = [json.loads(line) for line in output.splitlines()]
    summary = next(message['test_summary'] for message in messages if message['type'] == 'test_summary')
    assert summary['failed'] == 0 and summary['passed'] >= 3, summary
    state = next(message['test_state'] for message in messages if message['type'] == 'test_state')
    servers = {resource['index']: resource['values'] for resource in state['values']['root_module']['resources']
               if resource['type'] == 'hcloud_server'}
    assert sorted(servers) == list(ROLES), sorted(servers)
    return servers


def render(role, user_data):
    """Write the role's cloud-init files under build/infra/<role> and return the document."""
    header, document = user_data.split('\n', 1)
    assert header == '#cloud-config'
    cloud = json.loads(document)
    assert cloud['package_update'] and cloud['package_upgrade']
    assert cloud['runcmd'] == [['bash', '/usr/local/lib/runlink/initialize.sh']]
    fixture = RENDERED / role
    # Regenerate from scratch so files removed from the templates cannot linger.
    if fixture.exists():
        shutil.rmtree(fixture)
    fixture.mkdir(parents=True)
    (fixture / 'cloud-init.json').write_text(user_data)
    (fixture / 'cloud-init-schema.json').write_text(json.dumps(cloud))
    for file in cloud['write_files']:
        path = fixture / file['path'].lstrip('/')
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(file['content'])
        path.chmod(int(file['permissions'], 8))
        if path.suffix == '.sh' or path.name == 'runlink-tls':
            run('bash', '-n', path)
            run('shellcheck', path)
    return fixture


def check_cloud_init_schema(fixture):
    if not shutil.which('cloud-init'):
        print('cloud-init schema tool unavailable; checked structured content (run schema on Ubuntu before apply)')
        return
    # Call the pure schema validator, avoiding host datasource and state discovery.
    run('/usr/bin/python3', '-c', 'import json, sys; from cloudinit.config.schema import validate_cloudconfig_schema; '
        'validate_cloudconfig_schema(json.load(open(sys.argv[1])), strict=True)', fixture / 'cloud-init-schema.json')


def check_nftables(role, fixture):
    nft = shutil.which('nft')
    if not nft:
        print(f'{role}: nft binary unavailable; kernel validation remains on a disposable VM')
        return
    result = subprocess.run([nft, '--check', '--file', fixture / 'etc/nftables.conf'], capture_output=True, text=True)
    if result.returncode == 0:
        return
    # Without CAP_NET_ADMIN nft parses the rules, then fails at the kernel boundary.
    if 'Operation not permitted' not in result.stderr or 'syntax error' in result.stderr:
        raise SystemExit(f'{role}: nftables validation failed: {result.stderr}')
    print(f'{role}: nftables kernel validation requires CAP_NET_ADMIN on a disposable VM')


def check_systemd_units():
    if not shutil.which('systemd-analyze'):
        print('systemd-analyze unavailable; exact unit verification remains on Linux')
        return
    # Verify the exact units in a temporary root that provides only standard OS targets
    # and placeholder executables; verify never runs them.
    with tempfile.TemporaryDirectory() as temp:
        fixture = Path(temp)
        units = fixture / 'etc/systemd/system'
        units.mkdir(parents=True)
        names = []
        for unit in sorted(RENDERED.glob('*/etc/systemd/system/*.service')):
            shutil.copyfile(unit, units / unit.name)
            names.append(unit.name)
        for target in ('basic', 'sysinit', 'sockets', 'shutdown', 'network-online', 'multi-user'):
            (units / f'{target}.target').write_text('[Unit]\nDescription=Fixture OS target\n')
        (units / 'nftables.service').write_text('[Service]\nExecStart=/usr/bin/true\n')
        for name in ('usr/bin/python3', 'usr/bin/turnserver', 'usr/bin/true', 'opt/runlink/current/bin/runlink-server'):
            executable = fixture / name
            executable.parent.mkdir(parents=True, exist_ok=True)
            executable.write_text('#!/bin/sh\nexit 0\n')
            executable.chmod(0o755)
        run('systemd-analyze', 'verify', '--man=no', '--root', fixture, *names)


for role, values in mocked_servers().items():
    fixture = render(role, values['user_data'])
    check_cloud_init_schema(fixture)
    check_nftables(role, fixture)
check_systemd_units()
print('Infrastructure: provider validation, mocked stack, rendered cloud-init/shell/unit checks passed')
