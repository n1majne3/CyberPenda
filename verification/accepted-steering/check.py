"""Check Accepted Steering crash ordering without changing workspace files."""

import argparse
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import urllib.request


HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
TLC_VERSION = "1.7.4"
TLC_SHA256 = "936a262061c914694dfd669a543be24573c45d5aa0ff20a8b96b23d01e050e88"
TLC_URL = f"https://github.com/tlaplus/tlaplus/releases/download/v{TLC_VERSION}/tla2tools.jar"
TEST_NAME = "TestFormalAcceptedSteeringCrashDoesNotReplay"
SOURCE = ROOT / "internal/daemon/steering.go"


def checked_jar(path):
    if hashlib.sha256(path.read_bytes()).hexdigest() != TLC_SHA256:
        raise RuntimeError(f"TLC checksum mismatch: {path}; file was not changed")
    return path.resolve()


def tlc_jar(supplied):
    if supplied:
        return checked_jar(supplied)
    cache = Path(tempfile.gettempdir()) / f"cyberpenda-tlc-{TLC_VERSION}-{TLC_SHA256[:12]}"
    jar = cache / "tla2tools.jar"
    if jar.exists():
        return checked_jar(jar)
    # Downloads have their own empty directory, separate from scripts and runs.
    with tempfile.TemporaryDirectory(prefix="cyberpenda-tlc-download-") as directory:
        download = Path(directory) / "tla2tools.jar"
        print(f"Download pinned TLC {TLC_VERSION}: {TLC_URL}", flush=True)
        with urllib.request.urlopen(TLC_URL, timeout=60) as response:
            with download.open("xb") as output:
                shutil.copyfileobj(response, output)
        checked_jar(download)
        cache.mkdir(exist_ok=True)
        try:
            with jar.open("xb") as output:
                output.write(download.read_bytes())
        except FileExistsError:
            pass
    return checked_jar(jar)


def run(command, cwd):
    result = subprocess.run(command, cwd=cwd, text=True, encoding="utf-8", errors="replace",
                            stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=600)
    print(result.stdout, end="", flush=True)
    return result


def check_model(jar, scratch, broken=False):
    work = scratch / ("model-mutant" if broken else "model")
    work.mkdir()
    shutil.copyfile(HERE / "AcceptedSteering.tla", work / "AcceptedSteering.tla")
    config = (HERE / "AcceptedSteering.cfg").read_text(encoding="utf-8")
    if broken:
        config = replace_once(config, "FenceBeforeSend = TRUE", "FenceBeforeSend = FALSE")
    (work / "AcceptedSteering.cfg").write_text(config, encoding="utf-8")
    modules = work / "modules"
    modules.mkdir()
    print(f"\nTLC: {'fence-after-send mutation' if broken else 'current protocol'}", flush=True)
    result = run(["java", f"-Djava.io.tmpdir={modules}", "-XX:+UseParallelGC", "-Xmx256m", "-cp", str(jar), "tlc2.TLC",
                  "-workers", "1", "-fp", "0", "-seed", "1", "-metadir", str(work / "states"),
                  "-config", "AcceptedSteering.cfg", "AcceptedSteering.tla"], work)
    if broken:
        return result.returncode != 0 and "Invariant NoReplay is violated." in result.stdout
    return result.returncode == 0 and "Model checking completed. No error has been found." in result.stdout


def replace_once(text, old, new):
    if text.count(old) != 1:
        raise RuntimeError("Mutation anchor changed. Review the code mapping before updating the check.")
    return text.replace(old, new, 1)


def fence_after_send(source):
    start = source.index("\t\tfenced, err := server.steering.MarkDispatchStarted(")
    end = source.index("\t\t// Each provider delivery is bounded", start)
    fence = source[start:end]
    source = replace_once(source, fence, "")
    source = replace_once(source,
                          "adapter.execute(runCtx, fenced, continuationID, freshContinuation)",
                          "adapter.execute(runCtx, record, continuationID, freshContinuation)")
    return replace_once(source, "\t\tcancel()\n\t\tswitch execution.state {",
                        "\t\tcancel()\n" + fence + "\t\tswitch execution.state {")


def check_go(scratch, broken=False):
    work = scratch / ("go-mutant" if broken else "go")
    work.mkdir()
    virtual_test = ROOT / "internal/daemon/formal_accepted_steering_test.go"
    if virtual_test.exists():
        raise RuntimeError(f"Overlay target already exists: {virtual_test}; file was not changed")
    replacements = {str(virtual_test): str(HERE / "dispatch_test.go.txt")}
    if broken:
        mutation = work / "steering.go"
        mutation.write_text(fence_after_send(SOURCE.read_text(encoding="utf-8")), encoding="utf-8")
        replacements[str(SOURCE)] = str(mutation)
    overlay = work / "overlay.json"
    overlay.write_text(json.dumps({"Replace": replacements}), encoding="utf-8")
    print(f"\nGo: {'fence-after-send mutation' if broken else 'current code'}", flush=True)
    result = run(["go", "test", "-overlay", str(overlay), "./internal/daemon",
                  "-run", f"^{TEST_NAME}$", "-count=1", "-timeout=120s", "-v"], ROOT)
    if broken:
        return result.returncode != 0 and "NO_REPLAY:" in result.stdout
    return result.returncode == 0 and f"--- PASS: {TEST_NAME}" in result.stdout


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--self-test", action="store_true", help="also require model and real-code mutations to fail")
    parser.add_argument("--tlc-jar", type=Path, help="use a local pinned jar (the SHA-256 must match)")
    args = parser.parse_args()
    jar = tlc_jar(args.tlc_jar)
    # Only this invocation's generated files are removed. The workspace is read-only.
    with tempfile.TemporaryDirectory(prefix="cyberpenda-steering-check-") as directory:
        scratch = Path(directory)
        results = []
        if args.self_test:
            results += [check_model(jar, scratch, broken=True), check_go(scratch, broken=True)]
        results += [check_model(jar, scratch), check_go(scratch)]
        if not all(results):
            print("FAIL: verification or mutation sensitivity failed. See the output above.")
            return 1
    print("PASS: bounded model and real-code crash checks" +
          ("; both fence-after-send mutations were rejected." if args.self_test else "."))
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, RuntimeError, ValueError, subprocess.TimeoutExpired) as error:
        print(f"ERROR: {error}", file=sys.stderr)
        sys.exit(2)
