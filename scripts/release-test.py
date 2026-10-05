#!/usr/bin/env python3
"""Exercise release commands with actual binaries, ephemeral RSA keys and local roots."""
import hashlib
import io
import os
from pathlib import Path
import platform
import shutil
import subprocess
import tarfile
import tempfile
import time
import urllib.request

root = Path(__file__).resolve().parents[1]
release = root/'scripts/release.sh'
target = {'Linux':'linux','Darwin':'darwin'}[platform.system()]+'-'+{'x86_64':'amd64','arm64':'arm64','aarch64':'arm64'}[platform.machine()]

def call(*args, success=True, env=None):
    result = subprocess.run([str(release), *map(str,args)],capture_output=True,text=True,env=env)
    assert (result.returncode==0)==success, (args,result.stdout,result.stderr)
    return result

def resign(directory, key):
    (directory/'SHA256SUMS.sig').unlink()
    lines=''.join(f'{hashlib.sha256(path.read_bytes()).hexdigest()}  {path.name}\n' for path in sorted(directory.glob('*.tar.gz')))
    (directory/'SHA256SUMS').write_text(lines)
    call('sign',directory,'--private-key',key)

with tempfile.TemporaryDirectory(prefix='runlink-release-') as temp:
    fixture=Path(temp)
    key, public=fixture/'test.key',fixture/'test.pub'
    subprocess.run(['openssl','genpkey','-algorithm','RSA','-pkeyopt','rsa_keygen_bits:3072','-out',str(key)],check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    subprocess.run(['openssl','pkey','-in',str(key),'-pubout','-out',str(public)],check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    versions=['v0.0.1-fixture','v0.0.2-fixture']
    bundles=[]
    for version in versions:
        directory=fixture/version
        call('package','--version',version,'--platforms',target,'--output',directory)
        call('sign',directory,'--private-key',key)
        call('verify',directory,'--public-key',public)
        bundles.append(directory)
    install=fixture/'installation'
    def deploy(index, success=True, *extra):
        return call('deploy',bundles[index],'--root',install,'--version',versions[index],
                    '--platform',target,'--public-key',public,*extra,success=success)
    deploy(0)
    assert os.readlink(install/'current')=='releases/'+versions[0]
    run=subprocess.run([install/'current/bin/runlink','--version'],capture_output=True,text=True,check=True)
    assert versions[0] in run.stdout
    process=subprocess.Popen([install/'current/bin/runlink-server','--listen','127.0.0.1:18082',
                              '--state-dir',str(fixture/'state')],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    try:
        for _ in range(100):
            if process.poll() is not None:raise AssertionError('Release binary failed to start')
            try:
                with urllib.request.urlopen('http://127.0.0.1:18082/readyz',timeout=1):break
            except OSError:time.sleep(.05)
        else:raise AssertionError('Release binary readiness timed out')
        call('smoke','http://127.0.0.1:18082')
    finally:
        process.terminate();process.wait(timeout=5)
    # A failed smoke check restores the prior current link automatically.
    deploy(1,False,'--smoke-url','http://127.0.0.1:1')
    assert os.readlink(install/'current')=='releases/'+versions[0]
    # A separate install exercises successful update, explicit rollback, and immutability.
    install=fixture/'update'
    deploy(0);deploy(1)
    assert os.readlink(install/'current')=='releases/'+versions[1]
    call('rollback','--root',install,'--version',versions[0],'--platform',target,'--public-key',public)
    assert os.readlink(install/'current')=='releases/'+versions[0]
    deploy(0,False)
    installed=install/'releases'/versions[1]/'bin/runlink'
    original=installed.read_bytes();installed.write_bytes(original+b'tamper')
    call('rollback','--root',install,'--version',versions[1],'--platform',target,'--public-key',public,success=False)
    installed.write_bytes(original)
    corrupt=fixture/'corrupt';shutil.copytree(bundles[0],corrupt)
    archive=next(corrupt.glob('*.tar.gz'));archive.write_bytes(archive.read_bytes()+b'tamper')
    call('verify',corrupt,'--public-key',public,success=False)
    # A signed path traversal still cannot escape the local installation root.
    malicious=fixture/'malicious';shutil.copytree(bundles[0],malicious)
    archive=next(malicious.glob('*.tar.gz'))
    with tarfile.open(archive,'w:gz') as tar:
        info=tarfile.TarInfo('../../escaped');info.size=4;tar.addfile(info,io.BytesIO(b'evil'))
    resign(malicious,key)
    call('deploy',malicious,'--root',fixture/'unsafe','--version',versions[0],
         '--platform',target,'--public-key',public,success=False)
    assert not (fixture/'escaped').exists()
    broken=fixture/'bad-signature';shutil.copytree(bundles[0],broken)
    (broken/'SHA256SUMS.sig').write_bytes(b'invalid-signature')
    call('verify',broken,'--public-key',public,success=False)
    unsigned=fixture/'unsigned';shutil.copytree(bundles[0],unsigned)
    (unsigned/'extra.js').write_text('evil')
    call('verify',unsigned,'--public-key',public,success=False)
    # Test requested systemd actions with a temporary stub, never the host's manager.
    commands=fixture/'commands';commands.mkdir()
    log=fixture/'systemctl-actions.log'
    stub=commands/'systemctl'
    stub.write_text('#!/bin/sh\nprintf "%s %s\\n" "$1" "$2" >> "$RUNLINK_TEST_SYSTEMCTL_LOG"\n')
    stub.chmod(0o755)
    env={**os.environ, 'PATH':str(commands)+os.pathsep+os.environ['PATH'],
         'RUNLINK_TEST_SYSTEMCTL_LOG':str(log)}
    first_failure=fixture/'first-failure'
    call('deploy',bundles[0],'--root',first_failure,'--version',versions[0],
         '--platform',target,'--public-key',public,'--restart','runlink-frontend',
         '--smoke-url','http://127.0.0.1:1',success=False,env=env)
    assert not (first_failure/'current').is_symlink()
    assert log.read_text().splitlines()==['restart runlink-frontend','stop runlink-frontend']
    log.write_text('')
    restart_root=fixture/'restart-restore'
    call('deploy',bundles[0],'--root',restart_root,'--version',versions[0],
         '--platform',target,'--public-key',public,env=env)
    call('deploy',bundles[1],'--root',restart_root,'--version',versions[1],
         '--platform',target,'--public-key',public,'--restart','runlink-app',
         '--smoke-url','http://127.0.0.1:1',success=False,env=env)
    assert os.readlink(restart_root/'current')=='releases/'+versions[0]
    assert log.read_text().splitlines()==['restart runlink-app','restart runlink-app']
    # Repeated packaging has identical bytes despite wall-clock time and temporary paths.
    repeat=fixture/'repeat'
    call('package','--version',versions[0],'--platforms',target,'--output',repeat)
    assert next(repeat.glob('*.tar.gz')).read_bytes()==next(bundles[0].glob('*.tar.gz')).read_bytes()
print('Release: deterministic packaging, signatures, checksums, real binary smoke, update, rollback and tamper/traversal rejection passed')
