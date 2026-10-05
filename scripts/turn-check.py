#!/usr/bin/env python3
"""Run coturn UDP/TCP/TLS on loopback with ephemeral credentials and certificates."""
import base64
import hashlib
import hmac
import os
from pathlib import Path
import re
import shutil
import socket
import subprocess
import tempfile
import time
import sys

root=Path(__file__).resolve().parents[1]
server_binary=shutil.which('turnserver')
client_binary=shutil.which('turnutils_uclient')
if not server_binary or not client_binary:
    print('coturn runtime check unavailable on this platform; run on bootstrapped Ubuntu 26.04 amd64')
    raise SystemExit(0)
with tempfile.TemporaryDirectory(prefix='runlink-turn-') as temp:
    fixture=Path(temp)
    certificate,key=fixture/'certificate.pem',fixture/'private.key'
    subprocess.run(['openssl','req','-x509','-newkey','rsa:3072','-nodes','-keyout',str(key),
                    '-out',str(certificate),'-days','1','-subj','/CN=localhost',
                    '-addext','subjectAltName=DNS:localhost,IP:127.0.0.1'],check=True,
                   stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    # Keep production options, quotas and TLS version bounds; local addresses/ports
    # and loopback peer exceptions are fixture-only, never written into IaC.
    config=(root/'build/infra/c/etc/runlink/turnserver.base.conf').read_text()
    config=re.sub(r'^(listening-ip|relay-ip|allowed-peer-ip)=.*\n','',config,flags=re.M)
    config=config.replace('listening-port=3478','listening-port=13478').replace('tls-listening-port=5349','tls-listening-port=15349')
    config=config.replace('cert=/etc/runlink/tls/fullchain.pem',f'cert={certificate}')
    config=config.replace('pkey=/etc/runlink/tls/privkey.pem',f'pkey={key}')
    secret=os.urandom(32).hex()
    config+='\nlistening-ip=127.0.0.1\nrelay-ip=127.0.0.1\nallow-loopback-peers\nallowed-peer-ip=127.0.0.1\n'
    config=config.replace('/var/lib/runlink-turn/turn.sqlite',str(fixture/'turn.sqlite')).replace('/run/runlink-turn/turn.pid',str(fixture/'turn.pid'))
    config+=f'static-auth-secret={secret}\n'
    config+='relay-threads=1\n'
    # Execute the exact startup helper against a temporary filesystem layout.
    etc=fixture/'etc/runlink';runtime=fixture/'run/runlink-turn'
    etc.mkdir(parents=True);runtime.mkdir(parents=True)
    (etc/'turn-secret').write_text(secret+'\n')
    (etc/'turnserver.base.conf').write_text(config.split('static-auth-secret=')[0])
    helper=(root/'infra/templates/configure-turn.py').read_text()
    helper=helper.replace('/etc/runlink',str(etc)).replace('/run/runlink-turn',str(runtime))
    result=subprocess.run([sys.executable,'-c',helper],capture_output=True,text=True)
    assert result.returncode==0 and not result.stdout and not result.stderr
    merged=runtime/'turnserver.conf'
    assert merged.stat().st_mode&0o777==0o600
    assert merged.read_text().endswith('static-auth-secret='+secret+'\n')
    (etc/'turn-secret').write_text('invalid-secret\nstatic-auth-secret=injection')
    result=subprocess.run([sys.executable,'-c',helper],capture_output=True,text=True)
    assert result.returncode!=0 and 'injection' not in result.stdout+result.stderr
    assert merged.read_text().endswith('static-auth-secret='+secret+'\n')
    file=fixture/'turn.conf';file.write_text(config);file.chmod(0o600)
    server=subprocess.Popen([server_binary,'-c',str(file)],cwd=fixture,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    try:
        for _ in range(100):
            if server.poll() is not None:raise AssertionError('coturn exited during fixture startup')
            try:
                with socket.create_connection(('127.0.0.1',13478),timeout=.2):break
            except OSError:time.sleep(.05)
        else:raise AssertionError('coturn did not become ready')
        for index,(flags,port) in enumerate([([],13478),(['-t'],13478),(['-t','-S','-E',str(certificate)],15349)]):
            username=f'{int(time.time())+60}:fixture-{index}'
            password=base64.b64encode(hmac.new(secret.encode(),username.encode(),hashlib.sha1).digest()).decode()
            result=subprocess.run([client_binary,'-y','-c','-s','-z','100','-n','5','-m','2','-p',str(port),
                                   '-u',username,'-w',password,*flags,'127.0.0.1'],capture_output=True,text=True,timeout=20)
            # uclient can exit zero despite failed allocations; inspect packet results too.
            text=result.stdout+result.stderr
            assert result.returncode==0 and re.search(r'tot_send_msgs=10.*tot_recv_msgs=10',text), 'coturn fixture path failed: '+repr(re.findall(r'(?:tot_[a-z_]+|[a-z_]*msgs)=[0-9]+',text))
    finally:
        server.terminate()
        try:server.wait(timeout=5)
        except subprocess.TimeoutExpired:server.kill();server.wait()
print('coturn: bounded config parsed, ephemeral REST credentials, UDP/TCP/TLS same-server loopback relay passed (production firewall/NAT proof remains external)')
