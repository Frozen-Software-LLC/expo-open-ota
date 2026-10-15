"""Native protocol proof against the local Worker adapter, not Cloudflare.

Requires an isolated runtime 9090.1 APK configured for localhost:9090 and a
dedicated DealSeek_OTA_Combined emulator. Never uploads or publishes an OTA.
"""
import argparse
import hashlib
import json
import sqlite3
import subprocess
import time
from pathlib import Path

parser = argparse.ArgumentParser()
parser.add_argument('--root', required=True, type=Path)
parser.add_argument('--device', default='emulator-5580')
args = parser.parse_args()
root = args.root
app = 'com.dealseekorg.dealseek'
fixture = json.loads((root / 'fixture-a.json').read_text())


def adb(*values, check=True, timeout=30):
    return subprocess.run(['adb', '-s', args.device, *values], check=check,
                          capture_output=True, timeout=timeout).stdout


def shell(*values, check=True):
    return adb('shell', *values, check=check).decode().strip()


assert args.device.startswith('emulator-')
assert 'DealSeek_OTA_Combined' in adb('emu', 'avd', 'name').decode()
adb('reverse', 'tcp:9090', 'tcp:9090')
adb('reverse', 'tcp:9091', 'tcp:9091')
adb('install', '-r', str(root / 'DealSeek-local.apk'), timeout=120)


def database(out):
    names = shell('run-as', app, 'ls', 'databases', check=False).splitlines()
    name = next((n for n in names if 'updates' in n and
                 not n.endswith(('-wal', '-shm', '-journal'))), None)
    if not name:
        return None
    for suffix in ['', '-wal', '-shm']:
        destination = out / ('updates.db' + suffix)
        try:
            destination.write_bytes(adb('exec-out', 'run-as', app, 'cat', 'databases/' + name + suffix))
        except subprocess.CalledProcessError:
            destination.unlink(missing_ok=True)
    return sqlite3.connect(out / 'updates.db', timeout=.2)


def target_row(out):
    connection = None
    try:
        connection = database(out)
        if connection is None:
            return None
        return connection.execute(
            'select hex(id),status,successful_launch_count,failed_launch_count from updates where hex(id)=?',
            (fixture['targetId'].replace('-', '').upper(),)).fetchone()
    except sqlite3.Error:
        return None
    finally:
        if connection:
            connection.close()


def launch():
    shell('am', 'start', '-n', app + '/.MainActivity')


def events(path, offset):
    return [json.loads(line) for line in path.read_bytes()[offset:].decode().splitlines() if line.startswith('{')]


results = []
for case in ['cdn', 'cdn-missing', 'cdn-corrupt', 'cdn-unavailable']:
    out = root / ('android-' + case)
    out.mkdir(exist_ok=True)
    shell('am', 'force-stop', app)
    shell('pm', 'clear', app)
    adb('logcat', '-c')
    (root / 'state.json').write_text(json.dumps(dict(
        target=fixture['targetStorageId'], reloadEnabled=False, mode=case)))
    offset = (root / 'server.log').stat().st_size
    edge_offset = (root / 'edge.log').stat().st_size
    started = time.monotonic()
    launch()
    deadline = started + 60
    while time.monotonic() < deadline:
        row = target_row(out)
        if row and row[1] == 1:
            break
        time.sleep(.25)
    else:
        raise RuntimeError(f'{case}: download not ready: {row}')
    ready = time.monotonic() - started
    connection = database(out)
    asset = connection.execute(
        'select a.relative_path from assets a join updates u on u.launch_asset_id=a.id where hex(u.id)=?',
        (fixture['targetId'].replace('-', '').upper(),)).fetchone()[0]
    connection.close()
    data = adb('exec-out', 'run-as', app, 'cat', 'files/.expo-internal/' + asset)
    actual = hashlib.sha256(data).hexdigest()
    assert actual == fixture['sha256'], (case, actual)
    shell('am', 'force-stop', app)
    launch()
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        row = target_row(out)
        if row and row[2] > 0:
            break
        time.sleep(.25)
    else:
        raise RuntimeError(f'{case}: target did not launch: {row}')
    assert row[3] == 0, row
    bundles = [e for e in events(root / 'server.log', offset) if e['asset'].endswith('.hbc')]
    edge = events(root / 'edge.log', edge_offset)
    assert [e['status'] for e in bundles] == ([302] if case == 'cdn' else [302, 200]), bundles
    assert len(edge) == 1 and edge[0]['status'] == 200, edge
    if case != 'cdn':
        assert bundles[-1]['patchRequested'] == '', bundles
    result = dict(case=case, launchToDownloadReadySeconds=round(ready, 3),
                  targetSha256=actual, targetRow=row, bundleRequests=bundles, edgeRequests=edge,
                  totalBundleWireBytes=sum(e['bytes'] for e in bundles if e['status'] != 302) + sum(e['bytes'] for e in edge))
    (out / 'result.json').write_text(json.dumps(result, indent=2))
    (out / 'native-log.txt').write_bytes(adb('logcat', '-d', '-v', 'threadtime'))
    (out / 'screen.png').write_bytes(adb('exec-out', 'screencap', '-p'))
    results.append(result)
    print(json.dumps(result), flush=True)
(root / 'android-comparison.json').write_text(json.dumps(results, indent=2))
