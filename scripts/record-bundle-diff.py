"""Record real OTA transfers on a disposable, loopback-only iOS simulator.

Before --save-fresh, install/launch the isolated app with an empty server target
and dismiss OS prompts. Snapshots preserve that real app state for fair repeats.
No network delays or app progress indicators are injected.
"""
import argparse
import hashlib
import json
from pathlib import Path
import plistlib
import shutil
import signal
import sqlite3
import subprocess
import time

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--root", type=Path, required=True)
parser.add_argument("--device", required=True)
parser.add_argument("--save-fresh", action="store_true")
parser.add_argument("--save-existing", action="store_true")
parser.add_argument("--scenario", choices=["fresh", "existing"])
parser.add_argument("--mode", choices=["full", "normal"], default="normal")
args = parser.parse_args()
root, device = args.root.resolve(), args.device
app = "com.dealseekorg.dealseek"
settings = plistlib.loads((root / "DealSeek.app/Expo.plist").read_bytes())
assert settings["EXUpdatesURL"] == "http://127.0.0.1:9090/manifest"
assert settings["EXUpdatesRuntimeVersion"] == "9090.1"
inventory = json.loads(subprocess.check_output(["xcrun", "simctl", "list", "devices", "--json"]))
selected = next(d for ds in inventory["devices"].values() for d in ds if d["udid"] == device)
assert selected["name"].startswith("DealSeek DEV-9212")


def sim(*argv, check=True):
    return subprocess.run(["xcrun", "simctl", *argv], check=check, capture_output=True, text=True).stdout.strip()


container = Path(sim("get_app_container", device, app, "data"))
assert device in str(container)
internal = container / "Library/Application Support/.expo-internal"
db = internal / "expo-v11.db"


def snapshot(name):
    destination = root / (name + "-snapshot")
    destination.mkdir(exist_ok=True)
    for folder in ["Library", "Documents", "tmp"]:
        if (destination / folder).exists():
            shutil.rmtree(destination / folder)
        if (container / folder).exists():
            shutil.copytree(container / folder, destination / folder, symlinks=True)


def restore(name):
    source = root / (name + "-snapshot")
    assert source.is_dir()
    for folder in ["Library", "Documents", "tmp"]:
        if (container / folder).exists():
            shutil.rmtree(container / folder)
        if (source / folder).exists():
            shutil.copytree(source / folder, container / folder, symlinks=True)


def rows():
    try:
        with sqlite3.connect("file:" + str(db) + "?mode=ro", uri=True, timeout=0.1) as connection:
            return connection.execute("select hex(id),status,successful_launch_count,failed_launch_count from updates").fetchall()
    except sqlite3.Error:
        return []


def state(target, mode):
    destination = root / "state.json"
    temp = root / "state.next.json"
    temp.write_text(json.dumps({"target": target, "reloadEnabled": False, "mode": mode}))
    temp.replace(destination)


sim("terminate", device, app, check=False)
if args.save_fresh:
    embedded_id = json.loads((root / "fixture.json").read_text())["embeddedId"].replace("-", "").upper()
    assert all(row[0] == embedded_id for row in rows()), "Fresh snapshot must have only the embedded update"
    snapshot("fresh")
    print("Saved embedded-only baseline after OS prompt setup.")
    raise SystemExit(0)

if args.save_existing:
    expected = json.loads((root / "fixture.json").read_text())["targetId"].replace("-", "").upper()
    assert any(row[0] == expected and row[2] > 0 for row in rows())
    snapshot("existing")
    print("Saved existing-user baseline with OTA A installed.")
    raise SystemExit(0)

assert args.scenario
fixture = json.loads((root / ("fixture.json" if args.scenario == "fresh" else "fixture-b.json")).read_text())
restore("fresh" if args.scenario == "fresh" else "existing")
target_hex = fixture["targetId"].replace("-", "").upper()
assert not any(row[0] == target_hex for row in rows()), "Target must not already be cached"
output = root / "recordings" / (args.scenario + "-" + args.mode)
output.mkdir(parents=True, exist_ok=True)
video = output / "raw.mp4"
video.unlink(missing_ok=True)
log_offset = (root / "server.log").stat().st_size
state(fixture["targetStorageId"], args.mode)
record_log = (output / "recorder.log").open("w")
recorder = subprocess.Popen(["xcrun", "simctl", "io", device, "recordVideo", "--codec=h264", str(video)], stdout=record_log, stderr=record_log)
try:
    deadline = time.monotonic() + 10
    while "Recording started" not in (output / "recorder.log").read_text():
        if recorder.poll() is not None or time.monotonic() > deadline:
            raise RuntimeError("Simulator recorder did not start")
        time.sleep(0.05)
    started = time.monotonic()
    sim("launch", device, app)
    deadline = started + 45
    while time.monotonic() < deadline:
        row = next((r for r in rows() if r[0] == target_hex and r[1] == 1), None)
        if row:
            break
        time.sleep(0.1)
    else:
        raise RuntimeError("Target did not download")
    downloaded = time.monotonic() - started
    with sqlite3.connect(db) as connection:
        name = connection.execute("select a.relative_path from assets a join updates u on u.launch_asset_id=a.id where hex(u.id)=?", (target_hex,)).fetchone()[0]
    actual_hash = hashlib.sha256((internal / name).read_bytes()).hexdigest()
    expected_hash = hashlib.sha256((Path(fixture["targetDir"]) / fixture["asset"]).read_bytes()).hexdigest()
    assert actual_hash == expected_hash
    # Fixed viewing hold, not a delay in the network/update implementation.
    # The video labels the deliberate cold start; flag OFF is under test.
    while time.monotonic() - started < 10:
        time.sleep(0.1)
    cold_start = time.monotonic() - started
    sim("terminate", device, app, check=False)
    sim("launch", device, app)
    deadline = time.monotonic() + 35
    while time.monotonic() < deadline:
        row = next((r for r in rows() if r[0] == target_hex and r[2] > 0), None)
        if row:
            break
        time.sleep(0.1)
    else:
        raise RuntimeError("Patched target did not launch")
    assert row[3] == 0
    launched = time.monotonic() - started
    while time.monotonic() - started < max(24, launched + 8):
        time.sleep(0.1)
    lines = (root / "server.log").read_bytes()[log_offset:].decode().splitlines()
    events = [json.loads(line) for line in lines if line.startswith("{")]
    requests = [e for e in events if e.get("asset") == fixture["asset"]]
    assert [e["status"] for e in requests] == ([200] if args.mode == "full" else [226]), requests
    native = container / "Library/Application Support/dev.expo.modules.core.logging.expo-updates.txt"
    logs = native.read_text()
    if args.mode == "normal":
        assert "Applied diff" in logs
    (output / "native-log.txt").write_text(logs)
    result = {"scenario": args.scenario, "mode": args.mode, "fixture": fixture,
              "downloadReadySeconds": round(downloaded, 3), "manualColdStartSeconds": round(cold_start, 3),
              "successfulLaunchSeconds": round(launched, 3), "targetSha256": actual_hash,
              "targetRow": row, "bundleRequests": requests, "bundleWireBytes": sum(e["bytes"] for e in requests),
              "notes": "Loopback; real bytes; flag OFF; manual cold start after viewing hold; OS prompts handled before capture."}
    (output / "result.json").write_text(json.dumps(result, indent=2))
    sim("io", device, "screenshot", str(output / "screen.png"))
    print(json.dumps(result), flush=True)
finally:
    recorder.send_signal(signal.SIGINT)
    recorder.wait(timeout=15)
    record_log.close()
    sim("terminate", device, app, check=False)
if args.scenario == "fresh":
    snapshot("existing")
