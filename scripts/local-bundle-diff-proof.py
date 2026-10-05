"""Verify real full, patch, and corrupt-patch downloads on a disposable simulator."""
from pathlib import Path
import subprocess, json, time, sqlite3, hashlib, argparse, plistlib
parser = argparse.ArgumentParser(description='Run real OTA patch tests on a disposable local simulator')
parser.add_argument('--root', required=True, type=Path)
parser.add_argument('--device', required=True)
parser.add_argument('--cases', default='full,normal,corrupt')
args = parser.parse_args()
root = args.root
device = args.device
bundle = 'com.dealseekorg.dealseek'
fixture = json.loads((root / 'fixture.json').read_text())
settings = plistlib.loads((root / 'DealSeek.app/Expo.plist').read_bytes())
assert settings['EXUpdatesURL'] == 'http://127.0.0.1:9090/manifest' and settings['EXUpdatesRuntimeVersion'] == '9090.1', 'isolated test binary required'
inventory = json.loads(subprocess.check_output(['xcrun', 'simctl', 'list', 'devices', '--json'], text=True))
selected = next((d for ds in inventory['devices'].values() for d in ds if d['udid'] == device))
assert selected['name'].startswith('DealSeek DEV-9212'), 'use a disposable DealSeek DEV-9212 simulator'
cases = args.cases.split(',')
assert all((c in ['full', 'normal', 'corrupt'] for c in cases))

def sim(*args, check=True):
    return subprocess.run(['xcrun', 'simctl', *args], check=check, capture_output=True, text=True).stdout.strip()

def rows(container):
    native = container / 'Library/Application Support/dev.expo.modules.core.logging.expo-updates.txt'
    if not native.exists() or 'AppController appLoaderTask' not in native.read_text():
        return []
    p = container / 'Library/Application Support/.expo-internal/expo-v11.db'
    c = None
    try:
        c = sqlite3.connect('file:' + str(p) + '?mode=ro', uri=True, timeout=0.1)
        return c.execute('select hex(id),status,successful_launch_count,failed_launch_count from updates').fetchall()
    except sqlite3.Error:
        return []
    finally:
        if c:
            c.close()

def targetrow(container):
    return next((r for r in rows(container) if r[0].lower() == fixture['targetId'].replace('-', '')), None)
results = []
for case in cases:
    out = root / ('fresh-' + case + '-measured')
    out.mkdir(exist_ok=True)
    (root / 'state.json').write_text(json.dumps({'target': '', 'reloadEnabled': False, 'mode': case}))
    sim('terminate', device, bundle, check=False)
    sim('uninstall', device, bundle, check=False)
    sim('install', device, str(root / 'DealSeek.app'))
    container = Path(sim('get_app_container', device, bundle, 'data'))
    logOffset = (root / 'server.log').stat().st_size
    (root / 'state.json').write_text(json.dumps({'target': fixture['targetStorageId'], 'reloadEnabled': False, 'mode': case}))
    started = time.monotonic()
    sim('launch', device, bundle)
    deadline = started + 55
    while time.monotonic() < deadline:
        row = targetrow(container)
        if row and row[1] == 1:
            break
        time.sleep(0.1)
    else:
        raise RuntimeError(case + ' did not download: ' + str(rows(container)))
    downloaded = time.monotonic() - started
    c = sqlite3.connect(container / 'Library/Application Support/.expo-internal/expo-v11.db')
    name = c.execute('select a.relative_path from assets a join updates u on u.launch_asset_id=a.id where hex(u.id)=?', (fixture['targetId'].replace('-', '').upper(),)).fetchone()[0]
    c.close()
    actual = hashlib.sha256((container / 'Library/Application Support/.expo-internal' / name).read_bytes()).hexdigest()
    expected = hashlib.sha256((Path(fixture['targetDir']) / fixture['asset']).read_bytes()).hexdigest()
    assert actual == expected
    sim('terminate', device, bundle, check=False)
    sim('launch', device, bundle)
    deadline = time.monotonic() + 35
    while time.monotonic() < deadline:
        row = targetrow(container)
        if row and row[2] > 0:
            break
        time.sleep(0.2)
    else:
        raise RuntimeError(case + ' did not launch target: ' + str(rows(container)))
    assert row[3] == 0, (case, row)
    lines = (root / 'server.log').read_bytes()[logOffset:].decode().splitlines()
    events = [json.loads(l) for l in lines if l.startswith('{')]
    bundles = [e for e in events if '.hbc' in e['asset']]
    logs = (container / 'Library/Application Support/dev.expo.modules.core.logging.expo-updates.txt').read_text()
    selected = []
    for l in logs.splitlines():
        try:
            d = json.loads(l[l.index('{'):])
        except:
            continue
        if 'Updates state change' not in d.get('message', '') and (d.get('level') == 'error' or any((s in d.get('message', '').lower() for s in ['applied diff', 'patch failed']))):
            selected.append(d)
    result = {'case': case, 'launchToDownloadReadySeconds': round(downloaded, 3), 'bundleRequests': bundles, 'totalBundleWireBytes': sum((e['bytes'] for e in bundles)), 'targetSha256': actual, 'targetRow': row, 'nativeEvents': selected}
    if case == 'normal':
        assert [e['status'] for e in bundles] == [226]
        assert any(('Applied diff' in e.get('message', '') for e in selected))
    if case == 'full':
        assert [e['status'] for e in bundles] == [200]
    if case == 'corrupt':
        assert [e['status'] for e in bundles] == [226, 200]
    (out / 'result.json').write_text(json.dumps(result, indent=2))
    (out / 'native-log.txt').write_text(logs)
    sim('io', device, 'screenshot', str(out / 'screen.png'))
    results.append(result)
    print(json.dumps(result), flush=True)
(root / 'fresh-comparison.json').write_text(json.dumps(results, indent=2))
