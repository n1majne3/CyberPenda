#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""ctf-orchestrator 薄调度器:派发时注入的实现。

设计依据: docs/specs/hosted-dispatch-time-injection.md
状态唯一事实源是 $WS/graph/ledger.json;本脚本不调用任何模型。

子命令:
  init        从 challenges.json 初始化 ledger
  assemble    为一题组装下一段 execute prompt(含退场校验)写入 outbox
  validate    校验一份退场报告
  mark        标记一条 outbox 派发的状态(dispatched / infra_dead)
  harvest     扫描退场报告与看门狗,记账并按规则补位组装
  loop        周期执行 harvest + 平台对账(需 platform.sh)
  decide      写入一条升级决策(供 Decide 模型调用)
  selftest    在临时目录里验证核心路径
"""

import argparse
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import time
from pathlib import Path

DIFFICULTY_BUDGET_MIN = {"easy": 15, "medium": 25, "hard": 35, "chain": 60}
DEFAULT_QUOTA = 3
RESTART_CAP = 3
INFRA_DEATH_CAP = 4
ENDGAME_MARGIN_SEC = 300
WATCHDOG_GRACE_SEC = 180
SPAWN_ACK_SEC = 150
INJECT_CAP_CHARS = 8000
EXCLUDED_CAP_CHARS = 2400

FIXED_HEADER = """对授权靶场开展端到端渗透测试,入口目标为 {addr}。授权范围包括入口目标,以及测试过程中通过该入口、已建立会话、网络路由、目标响应或其他持久化证据确认属于同一靶场环境的内网资产;允许沿已验证攻击路径开展内网探测、横向移动和多阶段利用;不得访问无法证明与该靶场相关的外部或第三方资产。
目标:{addr}
题目:{name}
题目编号:{code}(本题共 {flags_total} 个 flag)"""

WORK_STYLE = """开工动作:先创建开工标记 {started_marker}(touch 一个空文件),再开始 recon。这是派发器确认投递成功的唯一早期信号。
工作方式:
1. 先用 recon 摸清入口(端口/服务/版本/目录/凭据线索),再按证据选攻击路径。
2. flag 通常在 /challenge/flag.txt,也可能在数据库、配置、环境变量、源码或内存里。
3. 找到 flag 后立刻在终端执行(把示例换成真实 flag,保留引号):submit-flag 'flag{{...}}'
4. 读返回 JSON 决定下一步:{{"correct":true,...}} → 已被接受;{{"code":"duplicate"}} → 之前已提交成功,同样算找到;{{"correct":false}} → 不对,回去继续找。
5. flag 大小写敏感(可能是 FLAG{{...}}),内部可能含特殊字符,提交时务必加引号。
6. 全局技巧:离线知识库 /opt/knowledge(hacktricks 方法论、payloads、wordlists),动手前 `pentest-knowledge-lookup <关键词>` 检索,命中文件整篇读完再动手。"""

EXIT_PROTOCOL = """【退场协议——本段结束前必须完成,否则结论不被采信】
1. 无论解出与否,把退场报告写到 {attempts_path},front-matter 必填:
   challenge / attempt / outcome / flags_gained / foothold / next_milestone
   outcome 只能取 solved / stalled / budget_stop 之一。
2. 正文两节:「试过的面(下一段不要重复)」与「证据指针」。大输出写
   graph/data/{code}-{attempt}-*.txt 或 .json,报告里只引用文件名。
3. 没有结果就如实写"已试X、观察到Y、未达成Z";禁止"此路不通/已穷尽/勿再试"类绝对结论。
4. 拿到 flag 立即提交,把返回 JSON 原文粘进退场报告。
5. 只连接本题目标地址,严禁扫描其他 IP(同网段常有其他目标在并行)。
6. 写完报告立即结束,最终回复只需一句"已收束于 {attempts_path}"。

本段预算 {budget_min} 分钟,到点必须收束(先写报告再结束)。"""

OUTCOMES = ("solved", "stalled", "budget_stop")


def now():
    return int(time.time())


def die(msg):
    sys.stderr.write("dispatch: %s\n" % msg)
    sys.exit(2)


# ---------------------------------------------------------------- workspace


class WS:
    def __init__(self, root):
        self.root = Path(root).resolve()
        self.ledger_path = self.root / "graph" / "ledger.json"
        self.attempts = self.root / "graph" / "attempts"
        self.outbox = self.root / "graph" / "outbox"
        self.escalations = self.root / "graph" / "escalations"
        self.facts = self.root / "graph" / "facts"
        self.deadline_path = self.root / "deadline"
        self.platform = self.root / "platform.sh"
        for d in (self.attempts, self.outbox, self.escalations, self.facts):
            d.mkdir(parents=True, exist_ok=True)

    def ledger(self):
        if not self.ledger_path.exists():
            return {"challenges": {}, "dispatches": {}, "seq": 0}
        return json.loads(self.ledger_path.read_text(encoding="utf-8"))

    def save_ledger(self, data):
        self.ledger_path.parent.mkdir(parents=True, exist_ok=True)
        tmp = self.ledger_path.with_suffix(".tmp")
        tmp.write_text(json.dumps(data, ensure_ascii=False, indent=1), encoding="utf-8")
        os.replace(tmp, self.ledger_path)

    def deadline(self):
        try:
            return int(self.deadline_path.read_text().strip())
        except (OSError, ValueError):
            return None

    def attempt_path(self, code, k):
        return self.attempts / code / ("%d.md" % k)

    def platform_call(self, *args):
        cmd = ["bash", str(self.platform)] + list(args)
        return subprocess.run(cmd, capture_output=True, text=True, timeout=120)


# ---------------------------------------------------------------- init


def parse_challenges(path):
    raw = json.loads(Path(path).read_text(encoding="utf-8"))
    items = raw["challenges"] if isinstance(raw, dict) and "challenges" in raw else raw
    out = []
    for c in items:
        code = c.get("challenge_code") or c.get("code") or c.get("id")
        if not code:
            continue
        flags = c.get("total_flag_count") or c.get("flag_count") or c.get("flags") or 1
        out.append({
            "code": str(code),
            "name": c.get("challenge_name") or c.get("name") or code,
            "score": c.get("challenge_score") or c.get("score") or 0,
            "difficulty": (c.get("difficulty") or "medium").lower(),
            "flags_total": int(flags),
            "addr": c.get("container_addr") or c.get("addr") or c.get("target") or "",
        })
    return out


def cmd_init(args):
    ws = WS(args.ws)
    challenges = parse_challenges(args.challenges)
    if not challenges:
        die("challenges.json 里没有可用条目")
    led = ws.ledger()
    for c in challenges:
        if c["code"] in led["challenges"]:
            continue
        budget = DIFFICULTY_BUDGET_MIN.get(c["difficulty"], 25)
        if c["flags_total"] > 1:
            budget = DIFFICULTY_BUDGET_MIN["chain"]
        led["challenges"][c["code"]] = dict(
            c, state="pending", attempt=0, restarts=0, infra_deaths=0,
            budget_min=budget, spent_min=0, flags_correct=0,
            last_fact_seq=0, instance=None, milestone=None)
    ws.save_ledger(led)
    print("init: %d 题入账(%d 新增)" % (len(led["challenges"]), len(challenges)))


# ---------------------------------------------------------------- exit reports


def parse_front_matter(text):
    m = re.match(r"^---\s*\n(.*?)\n---\s*\n", text, re.S)
    if not m:
        return None, text
    meta = {}
    for line in m.group(1).splitlines():
        if ":" in line:
            k, _, v = line.partition(":")
            meta[k.strip()] = v.strip()
    return meta, text[m.end():]


def validate_report(ws, code, k, quiet=True):
    """返回 (ok, reason, meta)。校验失败按 infra_dead 处理,不采信其结论。"""
    p = ws.attempt_path(code, k)
    if not p.exists():
        return False, "report_missing", None
    meta, body = parse_front_matter(p.read_text(encoding="utf-8"))
    if meta is None:
        return False, "front_matter_missing", None
    for key in ("challenge", "attempt", "outcome", "next_milestone"):
        if not meta.get(key):
            return False, "field_missing:" + key, None
    if meta["challenge"] != code:
        return False, "challenge_mismatch", None
    try:
        if int(meta["attempt"]) != k:
            return False, "attempt_mismatch", None
    except ValueError:
        return False, "attempt_not_int", None
    if meta["outcome"] not in OUTCOMES:
        return False, "outcome_invalid", None
    if meta["outcome"] == "solved" and not (
            '"correct": true' in body or '"correct":true' in body
            or '"code": "duplicate"' in body or '"code":"duplicate"' in body):
        return False, "solved_without_submit_json", None
    return True, "ok", meta


# ---------------------------------------------------------------- injection pack


def family_of(code):
    return code.split("-")[0]


def read_report_sections(path):
    meta, body = parse_front_matter(path.read_text(encoding="utf-8"))
    return meta or {}, body


def collect_family_knowledge(ws, code):
    """家族经验 = 已解同家族题的退场报告蒸馏 + graph/facts 里的同家族 fact。"""
    led = ws.ledger()
    pieces = []
    for other, rec in sorted(led["challenges"].items()):
        if other == code or family_of(other) != family_of(code):
            continue
        if rec.get("flags_correct", 0) <= 0:
            continue
        k = rec.get("attempt", 0)
        p = ws.attempt_path(other, k)
        if p.exists():
            meta, body = read_report_sections(p)
            pieces.append((other, "graph/attempts/%s/%d.md" % (other, k), body))
    if ws.facts.exists():
        fact_files = sorted(ws.facts.glob("*.md"))
        for p in fact_files:
            meta, _ = read_report_sections(p)
            fc = meta.get("challenge") or ""
            if fc and fc != code and family_of(fc) == family_of(code):
                rel = "graph/facts/%s" % p.name
                if all(rel != piece[1] for piece in pieces):
                    pieces.append((fc, rel, p.read_text(encoding="utf-8")))
    out = []
    used = 0
    for src, rel, body in pieces:
        trimmed = body.strip()
        if used + len(trimmed) > INJECT_CAP_CHARS and out:
            out.append("(%s 经验过长,读 %s 获取全文)" % (src, rel))
            continue
        out.append("【%s 已解经验 | 全文 %s】\n%s" % (src, rel, trimmed[:INJECT_CAP_CHARS]))
        used += len(trimmed)
    if not out:
        return "(该家族尚无已解经验。)"
    return "\n\n".join(out)


def collect_excluded(ws, code):
    led = ws.ledger()
    rec = led["challenges"].get(code, {})
    lines = []
    for k in range(1, rec.get("attempt", 0) + 1):
        p = ws.attempt_path(code, k)
        if not p.exists():
            continue
        meta, body = read_report_sections(p)
        if meta.get("outcome") == "infra_dead":
            continue
        m = re.search(r"#+\s*试过的面.*?\n(.*?)(?=\n#|\Z)", body, re.S)
        chunk = (m.group(1) if m else body).strip()
        if chunk:
            lines.append("第 %d 段(outcome=%s):\n%s" % (k, meta.get("outcome"), chunk))
    text = "\n".join(lines).strip()
    if not text:
        return "(首段,无历史。)"
    return text[:EXCLUDED_CAP_CHARS] + ("\n(超出截断,详见 graph/attempts/%s/)" % code
                                        if len(text) > EXCLUDED_CAP_CHARS else "")


def next_milestone(led, code):
    rec = led["challenges"][code]
    if rec.get("milestone"):
        return rec["milestone"]
    if rec["flags_total"] > 1:
        return "取得第 1 枚 flag,并在退场报告写明距下一枚 flag 的最短路径"
    return "解出本题(取得唯一 flag)"


# ---------------------------------------------------------------- assemble


def build_prompt(ws, led, code):
    rec = led["challenges"][code]
    k = rec["attempt"] + 1
    attempts_rel = "graph/attempts/%s/%d.md" % (code, k)
    started_rel = "graph/attempts/%s/%d.started" % (code, k)
    milestone = next_milestone(led, code)
    parts = [
        FIXED_HEADER.format(addr=rec.get("addr") or "(见平台 list 输出)",
                            name=rec["name"], code=code,
                            flags_total=rec["flags_total"]),
        "【本段目标】%s" % milestone,
        "【已排除的面】(来自此前各段退场报告,不要重复)\n%s" % collect_excluded(ws, code),
        "【家族经验】\n%s" % collect_family_knowledge(ws, code),
        WORK_STYLE.format(started_marker=started_rel),
        EXIT_PROTOCOL.format(attempts_path=attempts_rel, code=code,
                             attempt=k, budget_min=rec["budget_min"]),
    ]
    return k, attempts_rel, "\n\n".join(parts)


def assemble(ws, led, code, reason="manual"):
    """在调用方持有的 ledger 上变更;由调用方负责落盘,避免双写覆盖。"""
    rec = led["challenges"].get(code)
    if not rec:
        die("ledger 里没有 %s" % code)
    led["seq"] += 1
    did = "d%03d" % led["seq"]
    k, attempts_rel, prompt = build_prompt(ws, led, code)
    (ws.attempts / code).mkdir(parents=True, exist_ok=True)
    rec["attempt"] = k
    rec["state"] = "running"
    rec["milestone"] = None
    p = ws.outbox / ("%s-%s.prompt.md" % (did, code))
    p.write_text(prompt, encoding="utf-8")
    led["dispatches"][did] = {
        "code": code, "attempt": k, "state": "ready",
        "created": now(), "hard_stop": now() + rec["budget_min"] * 60,
        "reason": reason, "prompt": str(p.relative_to(ws.root)).replace("\\", "/"),
    }
    print("READY %s %s attempt=%d reason=%s prompt=%s" % (did, code, k, reason, p.name))
    return did


def _cli_assemble(args):
    ws = WS(args.ws)
    led = ws.ledger()
    assemble(ws, led, args.code)
    ws.save_ledger(led)


# ---------------------------------------------------------------- harvest / scheduling


def platform_flags(ws, code):
    if not ws.platform.exists():
        return None
    r = ws.platform_call("list")
    if r.returncode != 0:
        return None
    try:
        items = json.loads(r.stdout)
        if isinstance(items, dict):
            items = items.get("challenges", [])
    except ValueError:
        return None
    for c in items:
        cc = c.get("challenge_code") or c.get("code")
        if cc == code:
            return int(c.get("correct_flag_count") or 0)
    return None


def in_endgame(ws):
    dl = ws.deadline()
    return dl is not None and now() > dl - ENDGAME_MARGIN_SEC


DIFFICULTY_RANK = {"easy": 0, "medium": 1, "hard": 2}


def pick_next(led):
    """规则:新题 > 部分+foothold > 家族有新经验的 blocked;其余沉底。

    新题内部排序(命中率反馈回路,来自 21119 的教训——纯分值降序会让
    三棒全是多阶段硬题):已出分家族优先 > 未开家族;家族内先易后难,
    再按分值。"""
    fam_solved = {}
    for code, rec in led["challenges"].items():
        f = family_of(code)
        fam_solved[f] = fam_solved.get(f, 0) + (1 if rec.get("flags_correct") else 0)

    def rank(code, rec):
        if rec["state"] == "pending":
            proven = 0 if fam_solved.get(family_of(code), 0) else 1
            diff = DIFFICULTY_RANK.get(rec.get("difficulty"), 1)
            return (0, proven, diff, -rec["score"])
        if rec["state"] == "partial" and rec.get("foothold"):
            return (1, 0, 0, -rec["score"])
        if rec["state"] == "blocked" and rec.get("family_grew"):
            return (2, 0, 0, -rec["score"])
        return (3, 0, 0, -rec["score"])

    candidates = []
    for code, rec in led["challenges"].items():
        if rec["state"] in ("running", "solved", "exhausted"):
            continue
        if rec.get("restarts", 0) >= RESTART_CAP and not rec.get("flags_correct"):
            continue
        candidates.append((rank(code, rec), code))
    if not candidates:
        return None
    return sorted(candidates)[0][1]


def release_instance(ws, led, code, solved=None):
    """solved 题用 close;未完成题放槽用 abandon(不得 close 未完成题)。"""
    rec = led["challenges"][code]
    if rec.get("instance") and ws.platform.exists():
        if solved is None:
            solved = rec.get("flags_correct", 0) >= rec.get("flags_total", 1)
        if solved:
            ws.platform_call("close", code)
        else:
            ws.platform_call("abandon", code, "slot-release")
    rec["instance"] = None


def start_instance(ws, led, code):
    if not ws.platform.exists():
        return True, ""
    r = ws.platform_call("start", code)
    if r.returncode != 0:
        return False, (r.stderr or r.stdout)[:200]
    addr = ""
    m = re.search(r"[0-9.]+:\d+", r.stdout)
    if m:
        addr = m.group(0)
    led["challenges"][code]["instance"] = {"addr": addr, "since": now()}
    return True, ""


def account_report(ws, led, code, k, meta):
    rec = led["challenges"][code]
    outcome = meta["outcome"]
    try:
        gained = int(meta.get("flags_gained") or 0)
    except ValueError:
        gained = 0
    rec["flags_correct"] += gained
    rec["foothold"] = meta.get("foothold") or ""
    rec["milestone"] = meta.get("next_milestone") or None
    spent = max(0, int((now() - led["dispatches"].get(
        _last_dispatch(led, code), {}).get("created", now())) / 60) - 0)
    rec["spent_min"] += spent
    if outcome == "solved" or rec["flags_correct"] >= rec["flags_total"]:
        rec["state"] = "solved"
    elif outcome == "budget_stop":
        rec["restarts"] += 1
        rec["state"] = "exhausted" if rec["restarts"] >= RESTART_CAP else "partial"
    else:
        rec["restarts"] += 1
        if rec["restarts"] >= RESTART_CAP and not rec["flags_correct"]:
            rec["state"] = "exhausted"
        else:
            rec["state"] = "partial"
    if rec["state"] == "solved":
        release_instance(ws, led, code)
        _mark_family_grew(led, code)


def _last_dispatch(led, code):
    best, best_t = None, -1
    for did, d in led["dispatches"].items():
        if d["code"] == code and d.get("created", 0) > best_t:
            best, best_t = did, d["created"]
    return best


def _mark_family_grew(led, solved_code):
    fam = family_of(solved_code)
    for code, rec in led["challenges"].items():
        if family_of(code) == fam and rec["state"] == "blocked":
            rec["family_grew"] = True


def harvest(ws, quota=DEFAULT_QUOTA, dry=False):
    led = ws.ledger()
    actions = []
    # 1) 收割:有退场报告的 running 派发
    for did, d in list(led["dispatches"].items()):
        if d["state"] != "dispatched":
            continue
        code, k = d["code"], d["attempt"]
        ok, reason, meta = validate_report(ws, code, k)
        if ok:
            account_report(ws, led, code, k, meta)
            d["state"] = "done"
            actions.append("harvest %s %s attempt=%d outcome=%s" % (did, code, k, meta["outcome"]))
        elif reason != "report_missing":
            rec = led["challenges"][code]
            rec["state"] = "pending" if not rec["flags_correct"] else "partial"
            rec["infra_deaths"] += 1
            d["state"] = "invalid"
            actions.append("invalid-report %s %s reason=%s (按 infra_dead 记账)" % (did, code, reason))
    # 2) 看门狗两级:先查开工标记(投递失败的早期信号),再查 hard_stop
    for did, d in list(led["dispatches"].items()):
        if d["state"] != "dispatched":
            continue
        code, k = d["code"], d["attempt"]
        if (now() > d["created"] + SPAWN_ACK_SEC
                and not ws.attempt_path(code, k).with_suffix(".started").exists()):
            rec = led["challenges"][code]
            rec["infra_deaths"] += 1
            rec["state"] = "pending" if not rec["flags_correct"] else "partial"
            d["state"] = "infra_dead"
            if rec["infra_deaths"] >= INFRA_DEATH_CAP:
                escalate(ws, code, "infra_deaths=%d 连环死亡,封存或换资源?"
                         % rec["infra_deaths"])
                rec["state"] = "exhausted"
            actions.append("spawn-ack-miss %s %s infra_deaths=%d" % (did, code, rec["infra_deaths"]))
    for did, d in list(led["dispatches"].items()):
        if d["state"] != "dispatched":
            continue
        if now() > d["hard_stop"] + WATCHDOG_GRACE_SEC:
            code = d["code"]
            rec = led["challenges"][code]
            rec["infra_deaths"] += 1
            rec["state"] = "pending" if not rec["flags_correct"] else "partial"
            d["state"] = "infra_dead"
            if rec["infra_deaths"] >= INFRA_DEATH_CAP:
                escalate(ws, code, "infra_deaths=%d 连环死亡,封存或换资源?"
                         % rec["infra_deaths"])
                rec["state"] = "exhausted"
            actions.append("watchdog %s %s infra_deaths=%d" % (did, code, rec["infra_deaths"]))
    # 3) 平台周期对账(有 platform.sh 才做)
    if ws.platform.exists():
        for code, rec in led["challenges"].items():
            pf = platform_flags(ws, code)
            if pf is not None and pf > rec["flags_correct"]:
                rec["flags_correct"] = pf
                if pf >= rec["flags_total"] and rec["state"] != "solved":
                    rec["state"] = "solved"
                    release_instance(ws, led, code)
                    _mark_family_grew(led, code)
                    actions.append("reconcile %s flags=%d" % (code, pf))
    # 4) 补位:配额内组装下一段
    if not dry and not in_endgame(ws):
        running = sum(1 for r in led["challenges"].values() if r["state"] == "running")
        while running < quota:
            code = pick_next(led)
            if not code:
                break
            ok, err = start_instance(ws, led, code)
            if not ok:
                led["challenges"][code]["infra_deaths"] += 1
                actions.append("start-failed %s %s" % (code, err))
                if led["challenges"][code]["infra_deaths"] >= INFRA_DEATH_CAP:
                    led["challenges"][code]["state"] = "exhausted"
                break
            assemble(ws, led, code, reason="fill")
            running += 1
    else:
        for did, d in led["dispatches"].items():
            if d["state"] == "ready":
                d["state"] = "cancelled"
                actions.append("endgame-cancel %s" % did)
    ws.save_ledger(led)
    if in_endgame(ws):
        (ws.outbox / "ENDGAME").write_text("1\n", encoding="utf-8")
    for a in actions:
        print(a)
    return actions


def cmd_loop(args):
    ws = WS(args.ws)
    print("loop: interval=%ds quota=%d ws=%s" % (args.interval, args.quota, ws.root))
    while True:
        try:
            harvest(ws, quota=args.quota)
        except Exception as exc:  # noqa: BLE001 - 调度循环不允许退出
            print("loop error: %s" % exc)
        time.sleep(args.interval)


# ---------------------------------------------------------------- outbox / escalations


def cmd_mark(args):
    ws = WS(args.ws)
    led = ws.ledger()
    d = led["dispatches"].get(args.id)
    if not d:
        die("没有派发 %s" % args.id)
    if args.state not in ("dispatched", "infra_dead"):
        die("state 只能是 dispatched / infra_dead")
    d["state"] = args.state
    if args.state == "infra_dead":
        rec = led["challenges"][d["code"]]
        rec["state"] = "pending" if not rec["flags_correct"] else "partial"
    ws.save_ledger(led)
    print("mark %s %s" % (args.id, args.state))


def ready_lines(ws):
    led = ws.ledger()
    out = []
    for did, d in sorted(led["dispatches"].items()):
        if d["state"] == "ready":
            out.append("%s\t%s\t%s" % (did, d["code"], d["prompt"]))
    return out


def escalate(ws, code, question):
    led = ws.ledger()
    led.setdefault("escalations", {})
    led["escalations"].setdefault("next_id", 1)
    eid = "e%03d" % led["escalations"]["next_id"]
    led["escalations"]["next_id"] += 1
    led["escalations"][eid] = {"challenge": code, "question": question,
                               "state": "open", "created": now()}
    ws.save_ledger(led)
    p = ws.escalations / "QUEUE.md"
    with p.open("a", encoding="utf-8") as f:
        f.write("- %s [%s] %s\n" % (eid, code, question))
    return eid


def cmd_decide(args):
    ws = WS(args.ws)
    led = ws.ledger()
    e = led.get("escalations", {}).get(args.id)
    if not e:
        die("没有升级 %s" % args.id)
    e["state"] = "decided"
    e["decision"] = args.decision
    e["decided_at"] = now()
    code = e["challenge"]
    rec = led["challenges"].get(code)
    if rec and args.decision == "continue":
        rec["state"] = "partial"
        rec["restarts"] = max(0, rec.get("restarts", 0) - 1)
    elif rec and args.decision == "abandon":
        rec["state"] = "exhausted"
        release_instance(ws, led, code)
    ws.save_ledger(led)
    print("decide %s %s" % (args.id, args.decision))


def cmd_validate(args):
    ws = WS(args.ws)
    ok, reason, meta = validate_report(ws, args.code, args.attempt)
    print("%s %s" % ("OK" if ok else "FAIL", reason))
    sys.exit(0 if ok else 1)


# ---------------------------------------------------------------- selftest


def selftest():
    tmp = Path(tempfile.mkdtemp(prefix="dispatch-selftest-"))
    try:
        ws = WS(tmp)
        (tmp / "challenges.json").write_text(json.dumps({"challenges": [
            {"challenge_code": "a-01", "challenge_name": "T1", "challenge_score": 500,
             "difficulty": "hard", "total_flag_count": 1},
            {"challenge_code": "a-02", "challenge_name": "T2", "challenge_score": 300,
             "difficulty": "medium", "total_flag_count": 2},
            {"challenge_code": "b-01", "challenge_name": "T3", "challenge_score": 1200,
             "difficulty": "medium", "total_flag_count": 4},
        ]}), encoding="utf-8")
        cmd_init(argparse.Namespace(ws=str(tmp), challenges=str(tmp / "challenges.json")))
        led = ws.ledger()
        assert led["challenges"]["a-01"]["budget_min"] == 35
        assert led["challenges"]["a-02"]["budget_min"] == 60  # 多 flag → chain
        led = ws.ledger()
        did = assemble(ws, led, "a-01", reason="selftest")
        ws.save_ledger(led)
        assert ws.attempt_path("a-01", 1).parent.is_dir()  # assemble 预建了 attempts/<code>/
        prompt = (ws.outbox / ("%s-a-01.prompt.md" % did)).read_text(encoding="utf-8")
        assert "题目编号:a-01" in prompt and "退场协议" in prompt
        assert "graph/attempts/a-01/1.md" in prompt
        # 无报告 → 校验失败
        ok, reason, _ = validate_report(ws, "a-01", 1)
        assert not ok and reason == "report_missing"
        # 写一份合格报告 → 收割 + 补位
        p = ws.attempt_path("a-01", 1)
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text("---\nchallenge: a-01\nattempt: 1\noutcome: solved\n"
                     "flags_gained: 1\nfoothold: \"\"\nnext_milestone: \"\"\n---\n"
                     "# 试过的面\n- 登录绕过\n提交返回 {\"correct\": true}\n",
                     encoding="utf-8")
        ok, reason, meta = validate_report(ws, "a-01", 1)
        assert ok, reason
        led = ws.ledger()
        led["dispatches"][did]["state"] = "dispatched"
        ws.save_ledger(led)
        actions = harvest(ws, quota=2, dry=False)
        joined = "\n".join(actions)
        assert "outcome=solved" in joined
        led = ws.ledger()
        assert led["challenges"]["a-01"]["state"] == "solved"
        assert led["challenges"]["a-01"]["flags_correct"] == 1
        # a-01 解决后,同家族经验应能注入 a-02 的 prompt
        led = ws.ledger()
        did2 = assemble(ws, led, "a-02", reason="selftest2")
        ws.save_ledger(led)
        prompt2 = (ws.outbox / ("%s-a-02.prompt.md" % did2)).read_text(encoding="utf-8")
        assert "a-01 已解经验" in prompt2
        # solved 缺提交 JSON → 拒绝
        p2 = ws.attempt_path("a-02", 1)
        p2.parent.mkdir(parents=True, exist_ok=True)
        p2.write_text("---\nchallenge: a-02\nattempt: 1\noutcome: solved\n"
                      "next_milestone: x\n---\n没有 JSON\n", encoding="utf-8")
        ok, reason, _ = validate_report(ws, "a-02", 1)
        assert not ok and reason == "solved_without_submit_json"
        print("selftest: 全部通过")
    finally:
        shutil.rmtree(tmp, ignore_errors=True)


# ---------------------------------------------------------------- main


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    sub = ap.add_subparsers(dest="cmd", required=True)
    p = sub.add_parser("init")
    p.add_argument("--ws", required=True)
    p.add_argument("--challenges", required=True)
    p.set_defaults(func=cmd_init)
    p = sub.add_parser("assemble")
    p.add_argument("--ws", required=True)
    p.add_argument("code")
    p.set_defaults(func=_cli_assemble)
    p = sub.add_parser("validate")
    p.add_argument("--ws", required=True)
    p.add_argument("code")
    p.add_argument("attempt", type=int)
    p.set_defaults(func=cmd_validate)
    p = sub.add_parser("mark")
    p.add_argument("--ws", required=True)
    p.add_argument("id")
    p.add_argument("state")
    p.set_defaults(func=cmd_mark)
    p = sub.add_parser("harvest")
    p.add_argument("--ws", required=True)
    p.add_argument("--quota", type=int, default=DEFAULT_QUOTA)
    p.add_argument("--dry", action="store_true")
    p.set_defaults(func=lambda a: harvest(WS(a.ws), a.quota, a.dry))
    p = sub.add_parser("loop")
    p.add_argument("--ws", required=True)
    p.add_argument("--interval", type=int, default=20)
    p.add_argument("--quota", type=int, default=DEFAULT_QUOTA)
    p.set_defaults(func=cmd_loop)
    p = sub.add_parser("decide")
    p.add_argument("--ws", required=True)
    p.add_argument("id")
    p.add_argument("--decision", required=True,
                   choices=["continue", "abandon"])
    p.set_defaults(func=cmd_decide)
    sub.add_parser("selftest").set_defaults(func=lambda a: selftest())
    args = ap.parse_args()
    args.func(args)


if __name__ == "__main__":
    main()
