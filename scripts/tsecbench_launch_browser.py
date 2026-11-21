"""Browser / Playwright extension wizard helpers for tsecbench-launch.py."""

import glob
import json
import os
import re
import shutil
import subprocess
import sys
import time
from pathlib import Path

PLATFORM_URL = "https://tsecbench.zc.tencent.com/"
PW_SESSION = "tsec"
PW_NPX_GLOB = os.path.expandvars(
    r"%LOCALAPPDATA%\npm-cache\_npx/*/node_modules/playwright-core/cli.js"
)

# Playwright 扩展中继鉴权 token（可选）：从环境变量读取，或 --pw-token 传入。
PW_TOKEN = os.environ.get("PLAYWRIGHT_MCP_EXTENSION_TOKEN", "").strip()


def die(message, hint=""):
    print("error: %s" % message, file=sys.stderr)
    if hint:
        print(hint, file=sys.stderr)
    sys.exit(1)


def step(label):
    print("==> %s" % label)


def temp_config_path():
    return Path(__file__).parent / ".tsecbench-launch.pw.json"


def resolve_pw_cli():
    found = os.environ.get("PW_CLI")
    if found and os.path.exists(found):
        return found
    for candidate in sorted(glob.glob(PW_NPX_GLOB), reverse=True):
        if os.path.exists(candidate):
            return candidate
    return None


def resolve_node():
    node = shutil.which("node")
    if node:
        return node
    die(
        "找不到 node",
        "hint: playwright cli.js 需要 node 执行；确认 node 在 PATH，或设置环境变量",
    )


def pw(*args, timeout=60):
    cli = resolve_pw_cli()
    if not cli:
        die(
            "playwright-core cli not found",
            "hint: pass --token and --ovpn to skip the browser, "
            "or set PW_CLI to playwright-core/cli.js",
        )
    command = [resolve_node(), cli, "cli", "-s=" + PW_SESSION, "--json"] + list(args)
    env = dict(os.environ)
    if PW_TOKEN:
        env["PLAYWRIGHT_MCP_EXTENSION_TOKEN"] = PW_TOKEN
    try:
        result = subprocess.run(command, capture_output=True, text=True, timeout=timeout, env=env)
    except subprocess.TimeoutExpired:
        return 124, "", "command timed out after %ss: %s" % (timeout, args[0] if args else "")
    return result.returncode, result.stdout, result.stderr


def pw_kill_daemon():
    """杀掉 playwright 常驻 daemon：它可能是在没有 token 环境时拉起的，
    中继 URL 因此不带鉴权 token，扩展不会自动连接。"""
    script = (
        "Get-CimInstance Win32_Process -Filter \"Name='node.exe'\" | "
        "Where-Object { $_.CommandLine -match 'playwright-core' } | "
        "ForEach-Object { Stop-Process -Id $_.ProcessId -Force }"
    )
    subprocess.run(
        ["powershell", "-NoProfile", "-Command", script],
        capture_output=True, timeout=30,
    )


def pw_ensure_session():
    code, out, err = pw("tab-list", timeout=30)
    if code == 0:
        return
    if PW_TOKEN:
        step("重启 playwright 后台进程（带上鉴权 token，扩展将自动连接，无需点击）")
        pw_kill_daemon()
        time.sleep(1)
    else:
        step("正在请求 Edge 扩展连接（无 token，请在 Edge 弹出的页面点 Connect）")
    config = Path(temp_config_path())
    config.write_text(json.dumps({"extension": True}), encoding="utf-8")
    code, out, err = pw(
        "--config", str(config), "open", "--browser", "msedge", PLATFORM_URL, timeout=180,
    )
    if code == 0:
        return
    deadline = time.time() + 120
    while time.time() < deadline:
        code, out, err = pw("tab-list", timeout=30)
        if code == 0:
            print("    Edge 扩展已连接。")
            return
        print("    等待 Edge 扩展连接…")
        time.sleep(5)
    die(
        "Edge 扩展未连接",
        "hint: 若反复失败，确认 --pw-token 与 Playwright 扩展里显示的 token 一致；"
        "或用 --token / --ovpn 跳过浏览器",
    )


def pw_run_code(code, timeout=180):
    """执行 playwright run-code 片段，返回片段的返回值（JSON 解析后）。"""
    rc, out, err = pw("run-code", code, timeout=timeout)
    if rc != 0:
        die("run-code failed: %s" % ((err or out or "").strip()[:200]))
    try:
        parsed = json.loads(out)
    except ValueError:
        die("run-code output is not JSON: %s" % out[:200])
    result = parsed.get("result") if isinstance(parsed, dict) else parsed
    # CLI 对返回值做多层 JSON 编码：逐层剥到非字符串或解析失败为止。
    for _ in range(3):
        if not isinstance(result, str):
            return result
        try:
            result = json.loads(result)
        except ValueError:
            return result
    return result


def wizard_code(set_id, agent_name):
    """整段向导一个 run-code 片段完成：
    先深链 /benchmark/prepare/<set_id>?mode=local；冷启动 SPA 若把它重定向回首页，
    回落为完整点击路径（立即开始跑分 → 选择本地模式 → Tsecbench 卡片「选择并配置」），
    再选 Agent → 勾协议 → 「获取接入配置」→ 轮询 token（页面跳到 /benchmark/running/<id>）。"""
    prepare_url = json.dumps("%sbenchmark/prepare/%s?mode=local" % (PLATFORM_URL, set_id))
    home_url = json.dumps(PLATFORM_URL)
    agent_literal = json.dumps(agent_name, ensure_ascii=False)
    return (
        "async page => {"
        "const readToken = async () => {"
        "const t = await page.evaluate(() => document.body.innerText);"
        "const m = t.match(/BENCHMARK_TOKEN[\\s\uFF1A:]*([A-Za-z0-9][A-Za-z0-9-]{16,})/);"
        "return m ? m[1] : '';"
        "};"
        "const agentBtn = () => page.locator('button').filter({hasText:'请选择 Agent'}).first();"
        "await page.goto(" + prepare_url + ", {waitUntil:'domcontentloaded'});"
        "await page.waitForTimeout(2500);"
        "let token = await readToken();"
        "if (token) return JSON.stringify({mode:'reuse', token});"
        "if (await agentBtn().count() === 0) {"
        "await page.goto(" + home_url + ", {waitUntil:'domcontentloaded'});"
        "await page.waitForTimeout(2000);"
        "await page.locator('button').filter({hasText:'立即开始跑分'}).first().click();"
        "await page.waitForTimeout(1500);"
        "await page.locator('button, a').filter({hasText:'选择本地模式'}).first().click();"
        "await page.waitForTimeout(1500);"
        "await page.evaluate(() => {"
        "const cards=[...document.querySelectorAll('*')]"
        ".filter(e=>e.offsetParent!==null && (e.textContent||'').includes('Tsecbench v1')"
        " && (e.textContent||'').includes('选择并配置'));"
        "cards.sort((a,b)=>(a.textContent||'').length-(b.textContent||'').length);"
        "if(!cards[0]) return;"
        "const btn=[...cards[0].querySelectorAll('button,a')]"
        ".find(e=>e.textContent.replace(/\\s+/g,'').includes('选择并配置'));"
        "if(btn) btn.click();"
        "});"
        "await page.waitForTimeout(2000);"
        "token = await readToken();"
        "if (token) return JSON.stringify({mode:'reuse', token});"
        "}"
        "await agentBtn().waitFor({state:'visible', timeout:30000});"
        "await agentBtn().click();"
        "await page.waitForTimeout(1500);"
        "await page.locator('*').filter({hasText:new RegExp('^' + " + agent_literal + " + '$')}).last().click();"
        "await page.waitForTimeout(800);"
        "await page.evaluate(() => {"
        "const b=[...document.querySelectorAll('input[type=checkbox]')]"
        ".find(c=>c.offsetParent!==null && /协议/.test((c.closest('label')||c.parentElement||{}).textContent||''));"
        "if(b && !b.checked) b.click();"
        "});"
        "await page.waitForTimeout(500);"
        "await page.locator('button').filter({hasText:'获取接入配置'}).first().click();"
        "for (let i=0;i<25;i++) {"
        "await page.waitForTimeout(2000);"
        "token = await readToken();"
        "if (token) return JSON.stringify({mode:'created', token});"
        "await page.evaluate(() => {"
        "const b=[...document.querySelectorAll('button.view-tab')].find(e=>(e.textContent||'').trim()==='接入方式');"
        "if(b) b.click();"
        "});"
        "}"
        "return JSON.stringify({mode:'fail', url: page.url()});"
        "}"
    )


def run_platform_wizard(set_id, agent_name):
    """深链直达 TSecBench 接入配置页（/benchmark/prepare/<set_id>?mode=local）：
    若已有 run 会自动跳到 running 页，token 直接可读；否则一个 run-code 片段内完成
    选 Agent → 勾协议 → 点「获取接入配置」→ 轮询 token（页面跳到
    /benchmark/running/<id>，token 以明文出现）。"""
    result = pw_run_code(wizard_code(set_id, agent_name))
    if isinstance(result, dict) and result.get("token"):
        print("    run: %s" % result.get("mode"))
        return result["token"]
    die(
        "未能拿到 BENCHMARK_TOKEN: %s" % json.dumps(result, ensure_ascii=False)[:160],
        "hint: 平台可能拒绝创建（评测次数用尽/已有进行中 run）；"
        "或手动取 token 后 --token <值>",
    )


def pw_tabs():
    code, out, err = pw("tab-list")
    if code != 0:
        die("tab-list failed: %s" % err.strip()[:200])
    try:
        parsed = json.loads(out)
    except ValueError:
        die("tab-list output is not JSON: %s" % out[:200])
    result = parsed.get("result") if isinstance(parsed, dict) else parsed
    tabs = []
    if isinstance(result, str):
        for match in re.finditer(
            r"-\s*(\d+):\s*(?:\(([^)]*)\)\s*)?\[([^\]]*)\]\(([^)]*)\)", result
        ):
            tabs.append({
                "index": int(match.group(1)),
                "state": (match.group(2) or "").strip(),
                "title": match.group(3),
                "url": match.group(4),
            })
    elif isinstance(result, list):
        tabs = result
    return tabs


def fetch_token_from_edge(set_id, agent_name):
    step("从 Edge 深链直达 tsecbench 接入页（set %s）获取 BENCHMARK_TOKEN" % set_id)
    pw_ensure_session()
    tabs = pw_tabs()
    matches = [t for t in tabs if "tsecbench.zc.tencent.com" in str(t.get("url", ""))]
    if matches:
        pw("tab-select", str(matches[-1]["index"]))
    else:
        pw("tab-new", PLATFORM_URL, timeout=60)
    token = run_platform_wizard(set_id, agent_name)
    print("    token: %s…%s (len=%d)" % (token[:6], token[-4:], len(token)))
    return token


DOWNLOAD_OVPN_CODE = (
    # Edge 原生下载不触发 playwright 的 download 事件：只点按钮，由外层监听落盘。
    "async page => {"
    "const btn = page.locator('button, a').filter({ hasText: /下载\\s*VPN\\s*配置/ }).first();"
    "await btn.waitFor({ state: 'visible', timeout: 15000 });"
    "await btn.click();"
    "return 'clicked';"
    "}"
)


def download_fresh_vpn():
    """点击「下载VPN配置」，监听 Downloads 里新落的 task_*.ovpn。"""
    step("下载最新 VPN 配置")
    downloads = Path.home() / "Downloads"
    before = set(glob.glob(str(downloads / "task_*.ovpn")))
    code, out, err = pw("run-code", DOWNLOAD_OVPN_CODE, timeout=60)
    blob = (out or "") + (err or "")
    if "clicked" not in blob:
        print("    未找到「下载VPN配置」按钮（%s），回退到本地最新文件。" % blob.strip().replace("\n", " ")[:80])
        return None
    deadline = time.time() + 45
    while time.time() < deadline:
        fresh = [f for f in glob.glob(str(downloads / "task_*.ovpn")) if f not in before]
        fresh = [f for f in fresh if os.path.getsize(f) > 0]
        if fresh:
            path = max(fresh, key=os.path.getmtime)
            print("    ok: %s" % path)
            return path
        time.sleep(2)
    print("    45 秒内未见新 .ovpn，回退到本地最新文件。")
    return None
