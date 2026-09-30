"""Run the local Windows server with a reconnecting SSH tunnel.

Private runtime configuration lives in the ignored .dev directory.
"""
from pathlib import Path
import datetime
import json
import os
import socket
import subprocess
import sys
import time
import urllib.request

import psutil

ROOT = Path(__file__).resolve().parent.parent
STATE = ROOT / '.dev'
SCRIPT = Path(__file__).resolve()
BINARY = STATE / 'sub2api.exe'
SSH = Path(os.environ.get('WINDIR', r'C:\Windows')) / 'System32/OpenSSH/ssh.exe'
URL = 'http://127.0.0.1:8080'
STOP_FLAG = STATE / 'tunnel.stop'
DETACHED = subprocess.DETACHED_PROCESS | subprocess.CREATE_NEW_PROCESS_GROUP


def read_json(path):
    try:
        return json.loads(path.read_text(encoding='utf-8-sig'))
    except (FileNotFoundError, json.JSONDecodeError):
        return {}


def write_json(path, value):
    temporary = path.with_suffix('.tmp')
    temporary.write_text(json.dumps(value, indent=2) + '\n', encoding='utf-8')
    temporary.replace(path)


def process_record(process):
    return {'pid': process.pid, 'created': psutil.Process(process.pid).create_time()}


def owned_process(record, kind):
    if isinstance(record, int):
        record = {'pid': record}
    try:
        process = psutil.Process(record['pid'])
        if 'created' in record and abs(process.create_time() - record['created']) > 0.01:
            return None
        if kind == 'backend':
            valid = Path(process.exe()).resolve() == BINARY.resolve()
        elif kind == 'supervisor':
            command = process.cmdline()
            valid = str(SCRIPT) in command and 'supervise' in command
        else:
            command = process.cmdline()
            valid = Path(process.exe()).resolve() == SSH.resolve() and str(STATE / 'ssh-tunnel.config') in command
        return process if valid and process.is_running() else None
    except (KeyError, psutil.Error):
        return None


def launch(arguments, name, **kwargs):
    with (STATE / f'{name}.stdout.log').open('ab') as output, (STATE / f'{name}.stderr.log').open('ab') as errors:
        return subprocess.Popen(arguments, stdin=subprocess.DEVNULL, stdout=output,
                                stderr=errors, close_fds=True, creationflags=DETACHED, **kwargs)


def ports_ready():
    for port in (15432, 16379):
        try:
            with socket.create_connection(('127.0.0.1', port), timeout=1):
                pass
        except OSError:
            return False
    return True


def supervise():
    targets = read_json(STATE / 'tunnel-targets.json')
    arguments = [str(SSH), '-F', str(STATE / 'ssh-tunnel.config'), '-N', '-T',
                 '-L', targets['postgres'], '-L', targets['redis'], 'poly-v3-local']
    supervisor = process_record(psutil.Process())
    while not STOP_FLAG.exists():
        tunnel = launch(arguments, 'tunnel')
        write_json(STATE / 'tunnel-processes.json', {'supervisor': supervisor, 'tunnel': process_record(tunnel)})
        while tunnel.poll() is None and not STOP_FLAG.exists():
            time.sleep(1)
        if tunnel.poll() is None:
            tunnel.terminate()
            tunnel.wait(timeout=10)
        if not STOP_FLAG.exists():
            print(f'{datetime.datetime.now().isoformat()} SSH exited ({tunnel.returncode}); reconnecting.', flush=True)
            for _ in range(3):
                if STOP_FLAG.exists():
                    break
                time.sleep(1)


def stop_backend():
    record = read_json(STATE / 'local-processes.json')
    backend = owned_process(record.get('backend', {}), 'backend')
    if backend:
        backend.terminate()
        backend.wait(timeout=15)
        print(f'Stopped local backend {backend.pid}.')


def stop():
    STOP_FLAG.touch()
    record = read_json(STATE / 'tunnel-processes.json')
    supervisor = owned_process(record.get('supervisor', {}), 'supervisor')
    if supervisor:
        try:
            supervisor.wait(timeout=5)
        except psutil.TimeoutExpired:
            supervisor.terminate()
            supervisor.wait(timeout=5)
    tunnel = owned_process(record.get('tunnel', {}), 'tunnel')
    if tunnel:
        tunnel.terminate()
        tunnel.wait(timeout=5)
    stop_backend()
    print('Local server and SSH supervisor stopped.')


def start():
    for name in ('local-runtime.json', 'tunnel-targets.json', 'ssh-tunnel.config', 'sub2api.exe'):
        if not (STATE / name).is_file():
            raise RuntimeError(f'Missing local deployment file: .dev/{name}')
    STOP_FLAG.unlink(missing_ok=True)
    tunnels = read_json(STATE / 'tunnel-processes.json')
    supervisor = owned_process(tunnels.get('supervisor', {}), 'supervisor')
    if not supervisor:
        supervisor = launch([sys.executable, str(SCRIPT), 'supervise'], 'supervisor')
    for _ in range(45):
        if ports_ready():
            break
        time.sleep(1)
    else:
        raise RuntimeError('SSH tunnel did not connect; see .dev/tunnel.stderr.log.')
    previous = read_json(STATE / 'local-processes.json')
    backend = owned_process(previous.get('backend', {}), 'backend')
    if not backend:
        environment = os.environ.copy()
        environment.update({k: str(v) for k, v in read_json(STATE / 'local-runtime.json').items()})
        for name in list(environment):
            if name.upper() in {'HTTPS_PROXY', 'HTTP_PROXY', 'ALL_PROXY'}:
                environment.pop(name)
        backend = launch([str(BINARY)], 'backend', cwd=ROOT / 'backend', env=environment)
    backend_record = process_record(backend)
    write_json(STATE / 'local-processes.json', {'backend': backend_record, 'url': URL,
                                              'started_at': datetime.datetime.now().isoformat()})
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    deadline = time.monotonic() + 180
    while time.monotonic() < deadline:
        if not owned_process(backend_record, 'backend'):
            raise RuntimeError('Backend exited; see .dev/backend.stdout.log and backend.stderr.log.')
        try:
            for endpoint in ('/health', '/api/v1/settings/public'):
                with opener.open(URL + endpoint, timeout=3) as response:
                    if response.status != 200:
                        raise RuntimeError('Local API is not ready.')
            print(f'Local server ready: {URL}\nBackend PID: {backend.pid}; SSH supervisor PID: {supervisor.pid}')
            return
        except (OSError, RuntimeError):
            time.sleep(1)
    raise RuntimeError('Backend or database readiness timed out; see .dev/backend.stdout.log.')


if __name__ == '__main__':
    actions = {'start': start, 'stop': stop, 'stop-backend': stop_backend, 'supervise': supervise}
    try:
        actions[sys.argv[1]]()
    except Exception as error:
        print(str(error), file=sys.stderr)
        sys.exit(1)
