#!/usr/bin/env python3
"""TSecBench 一键启动器。

把「平台开向导拿 token → 下载 VPN 配置 → CyberPenda 改凭证 → 新建 Session → 选 Profile → Launch」
压缩成一条命令：

    python scripts/tsecbench-launch.py

全自动路径（默认）：脚本驱动已登录的 Edge（playwright cli extension 模式）自动走完
tsecbench 向导（立即开始跑分 → 选择本地模式 → 选择评测集 → 勾协议 → 获取接入配置），
读出 BENCHMARK_TOKEN，点击「下载VPN配置」拿最新 .ovpn，然后经 daemon API 更新凭证并
创建/启动 Session（Runtime Profile 默认 tsecbench）。

唯一的手工动作：当 playwright 后台进程已死（通常在重启机器后第一次运行）时，Edge 会弹出
扩展连接确认页，需要人点一次 Connect；点过之后本轮及后续重跑都不再需要。

跳过浏览器的等价用法：
    python scripts/tsecbench-launch.py --token XXX             # 手动给 token
    python scripts/tsecbench-launch.py --token XXX --ovpn PATH # 连 VPN 文件也手动给
    python scripts/tsecbench-launch.py --dry-run               # 彩排，不写任何状态
    python scripts/tsecbench-launch.py --check                 # 只看状态

环境变量：
    PENTEST_AUTH_TOKEN（或 --auth-token）：daemon operator 鉴权 token（Bearer）。
    PLAYWRIGHT_MCP_EXTENSION_TOKEN（或 --pw-token）：扩展中继鉴权 token，可选。
"""

import argparse
import glob
import json
import os
import subprocess
import sys
import time
from pathlib import Path

_SCRIPT_DIR = str(Path(__file__).resolve().parent)
if _SCRIPT_DIR not in sys.path:
    sys.path.insert(0, _SCRIPT_DIR)

import tsecbench_launch_browser as browser
from tsecbench_launch_browser import (
    DOWNLOAD_OVPN_CODE,
    PLATFORM_URL,
    PW_NPX_GLOB,
    PW_SESSION,
    PW_TOKEN,
    die,
    download_fresh_vpn,
    fetch_token_from_edge,
    pw,
    pw_ensure_session,
    pw_kill_daemon,
    pw_run_code,
    pw_tabs,
    resolve_node,
    resolve_pw_cli,
    run_platform_wizard,
    step,
    temp_config_path,
    wizard_code,
)

DEFAULT_DAEMON = os.environ.get("PENTEST_DAEMON_URL", "http://127.0.0.1:8787").rstrip("/")
DEFAULT_PROFILE_NAME = "tsecbench"
DEFAULT_SET_ID = "4"  # Tsecbench v1（/benchmark/prepare/<set_id>）
DEFAULT_AGENT_NAME = "CyberPenda"  # 平台 AGENT 信息下拉里的选项
CRED_REF = "BENCHMARK_TOKEN"
AUTH_TOKEN = os.environ.get("PENTEST_AUTH_TOKEN", "").strip()

DEFAULT_GOAL = """# 开始
先执行 VPN 联通预检（curl http://10.0.100.58 status:"ok"）；通过后再调用 GET /openapi/v1/challenges 获取题目列表，然后按上述流程逐题推进。预检不通过则立即中断并提示「VPN检测未通过,请检查靶场VPN网络配置」，不要进入跑分流程。
使用 tsecbench skills, vpn 在附件里"""


def _curl(args, timeout=60):
    command = ["curl", "-s", "--noproxy", "*", "--max-time", str(timeout), "-w", "\n%{http_code}"]
    stdin_bytes = None
    if AUTH_TOKEN:
        escaped = AUTH_TOKEN.replace("\\", "\\\\").replace('"', '\\"')
        command += ["-K", "-"]
        stdin_bytes = ('header = "Authorization: Bearer %s"\n' % escaped).encode("utf-8")
    command += args
    result = subprocess.run(command, input=stdin_bytes, capture_output=True, timeout=timeout + 10)
    output = result.stdout.decode("utf-8", "replace")
    if "\n" not in output:
        die("curl failed: %s" % (result.stderr.decode("utf-8", "replace")[:200] or output[:200]))
    body, _, status = output.rpartition("\n")
    return status.strip(), body


def _auth_hint(status):
    if status == "401" and not AUTH_TOKEN:
        return "hint: daemon 需要 operator token，请设置 PENTEST_AUTH_TOKEN 或传入 --auth-token"
    return ""


def http_json(method, url, body=None, timeout=30):
    args = ["-X", method, url]
    if body is not None:
        args += ["-H", "Content-Type: application/json", "-d", json.dumps(body, ensure_ascii=False)]
    last_error = None
    for attempt in range(4):
        try:
            status, text = _curl(args, timeout=timeout)
            if status.startswith("2"):
                return json.loads(text)
            die("%s %s -> HTTP %s: %s" % (method, url, status, text[:400]), _auth_hint(status))
        except subprocess.TimeoutExpired as exc:
            last_error = exc
            time.sleep(0.5 * (attempt + 1))
        except ValueError:
            die("%s %s returned non-JSON: %s" % (method, url, text[:200]))
    die("%s %s failed: %s" % (method, url, last_error))


def newest_ovpn():
    downloads = Path.home() / "Downloads"
    candidates = glob.glob(str(downloads / "task_*.ovpn"))
    if not candidates:
        candidates = glob.glob(str(downloads / "*.ovpn"))
    if not candidates:
        return None
    return max(candidates, key=os.path.getmtime)


def resolve_profile(daemon, name):
    profiles = http_json("GET", daemon + "/api/runtime-profiles")
    if isinstance(profiles, dict):
        profiles = profiles.get("profiles") or profiles.get("items") or []
    for profile in profiles:
        if profile.get("name") == name:
            return profile
    die(
        "Runtime Profile %r 不存在" % name,
        "hint: 现有 profiles: " + ", ".join(sorted(p.get("name", "?") for p in profiles)),
    )


def update_credential(daemon, token, dry_run):
    step("更新凭证 %s" % CRED_REF)
    body = {
        "credential_ref": CRED_REF,
        "source": {"kind": "literal", "value": token, "destination_env": CRED_REF},
        "disabled": False,
    }
    if dry_run:
        print("    [dry-run] PUT /api/credential-bindings credential_ref=%s value=***" % CRED_REF)
        return
    binding = http_json("PUT", daemon + "/api/credential-bindings", body)
    print("    ok: %s -> %s" % (CRED_REF, binding.get("source", {}).get("value", "?")))


def create_session(daemon, profile_id, goal, ovpn_path, dry_run):
    step("创建 Session（profile=%s, attachment=%s）" % (profile_id, os.path.basename(ovpn_path)))
    payload = {
        "input": goal,
        "runtime_profile_id": profile_id,
        "blackboard_mode": "disabled",
        # 靶场走 OpenVPN：tun0 需要容器拿到 /dev/net/tun + NET_ADMIN（UI 的 VPN TUN 勾选）
        "run_controls": {
            "blackboard_mode": "disabled",
            "sandbox_vpn_tun": True,
        },
    }
    if dry_run:
        print("    [dry-run] POST /api/sessions multipart payload=%s" % json.dumps(payload, ensure_ascii=False)[:200])
        print("    [dry-run] would attach: %s" % ovpn_path)
        return None
    payload_json = json.dumps(payload, ensure_ascii=False)
    field_args = [
        "-F", "payload=" + payload_json + ";type=application/json",
        "-F", "attachments=@%s" % ovpn_path,
    ]
    request_args = ["-X", "POST", daemon + "/api/sessions"] + field_args
    created = None
    last_error = None
    for attempt in range(4):
        try:
            status, text = _curl(request_args, timeout=120)
            if status.startswith("2"):
                created = json.loads(text)
                break
            die("create session -> HTTP %s: %s" % (status, text[:400]), _auth_hint(status))
        except subprocess.TimeoutExpired as exc:
            last_error = exc
            time.sleep(0.5 * (attempt + 1))
        except ValueError:
            die("create session returned non-JSON: %s" % text[:200])
    if created is None:
        die("create session failed: %s" % last_error)
    session_id = created.get("id", "?")
    print("    ok: session %s" % session_id)
    print("    %s/sessions/%s" % (daemon, session_id))
    return session_id


def main():
    parser = argparse.ArgumentParser(description="TSecBench 一键启动器")
    parser.add_argument("--token", help="手动指定 BENCHMARK_TOKEN（跳过浏览器读取）")
    parser.add_argument("--ovpn", help="手动指定 VPN .ovpn 配置文件（默认取 Downloads 最新 task_*.ovpn）")
    parser.add_argument("--goal-file", help="用文件内容作为 Session goal（默认内置模板）")
    parser.add_argument("--profile", default=DEFAULT_PROFILE_NAME, help="Runtime Profile 名称")
    parser.add_argument("--set-id", default=DEFAULT_SET_ID, help="tsecbench 评测集 id（URL /benchmark/prepare/<id>）")
    parser.add_argument("--agent", default=DEFAULT_AGENT_NAME, help="平台 AGENT 信息下拉选择的 Agent 名称")
    parser.add_argument("--daemon", default=DEFAULT_DAEMON)
    parser.add_argument("--auth-token", default="", help="Daemon operator token（默认读 PENTEST_AUTH_TOKEN 环境变量）")
    parser.add_argument("--pw-token", default="", help="Playwright 扩展中继 token（默认读 PLAYWRIGHT_MCP_EXTENSION_TOKEN 环境变量）")
    parser.add_argument("--dry-run", action="store_true", help="只打印动作，不写任何状态")
    parser.add_argument("--check", action="store_true", help="只检查 daemon/profile 状态")
    args = parser.parse_args()
    global AUTH_TOKEN, PW_TOKEN
    if args.auth_token:
        AUTH_TOKEN = args.auth_token.strip()
    if args.pw_token:
        PW_TOKEN = args.pw_token.strip()
        browser.PW_TOKEN = PW_TOKEN

    step("检查 daemon %s" % args.daemon)
    http_json("GET", args.daemon + "/api/runtime-profiles")
    profile = resolve_profile(args.daemon, args.profile)
    print("    profile: %s (%s)" % (profile.get("name"), profile.get("id")))

    if args.check:
        bindings = http_json("GET", args.daemon + "/api/credential-bindings").get("bindings", [])
        configured = [b for b in bindings if b.get("credential_ref") == CRED_REF]
        state = "configured" if configured else "NOT configured"
        print("    credential %s: %s" % (CRED_REF, state))
        print("    newest vpn in Downloads: %s" % (newest_ovpn() or "none"))
        return

    if args.dry_run:
        # 纯本地预演：不碰浏览器（向导会真实创建 run），不更新凭证，不建 session。
        token = args.token or "<dry-run-token>"
        ovpn = args.ovpn or newest_ovpn()
        goal = Path(args.goal_file).read_text(encoding="utf-8") if args.goal_file else DEFAULT_GOAL
        print("    [dry-run] 跳过浏览器向导（不会在平台创建 run）")
        update_credential(args.daemon, token, True)
        create_session(args.daemon, profile.get("id"), goal, ovpn or "<none>", True)
        print("dry-run 完成，未写入任何状态。")
        return

    browser_used = False
    if args.token:
        token = args.token
    else:
        token = fetch_token_from_edge(args.set_id, args.agent)
        browser_used = True

    ovpn = args.ovpn
    if not ovpn:
        ovpn = (download_fresh_vpn() if browser_used else None) or newest_ovpn()
    if not ovpn or not os.path.exists(ovpn):
        die(
            "没有可用的 .ovpn",
            "hint: 让脚本自动下载（浏览器路径默认会下）或在平台页手动下载后重试，或 --ovpn <路径>",
        )
    print("    vpn config: %s" % ovpn)

    goal = DEFAULT_GOAL
    if args.goal_file:
        goal = Path(args.goal_file).read_text(encoding="utf-8")

    update_credential(args.daemon, token, args.dry_run)
    created = create_session(args.daemon, profile.get("id"), goal, ovpn, args.dry_run)
    if args.dry_run:
        print("dry-run 完成，未写入任何状态。")
    elif created:
        print("已启动。Runtime 首次调用平台 API 后平台侧任务自动进入「进行中」。")


if __name__ == "__main__":
    main()
