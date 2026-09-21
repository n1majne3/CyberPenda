---
name: tsecbench-fetch
description: 通过 TSecBench 平台只读 API 拉取跑分(run)与会话(session)数据:agent 详情、积分时间线、LLM 会话清单与逐事件明细、榜单。凡用户给出 tsecbench.zc.tencent.com/agent/<id> 链接、提到"跑分记录/会话记录/拉数据/复盘/对比跑分"、或要分析某次 Hosted 跑分的 token 消耗、调用节奏、瓶颈时,使用本 skill。
---

# TSecBench 跑分数据拉取

平台页面只展示聚合视图;完整数据在只读 API 里。API 需要 Bearer token,
最省事的取法是借 playwright-cli 挂到已登录的 Edge 上,在页面上下文里发 fetch。

## 前置:挂上浏览器

```bash
playwright-cli attach --extension=msedge
```

之后所有命令带 `-s=msedge`。若已挂载则跳过。用户多半用 Edge;
登录态在 localStorage 的 `token` 键里。

## 第一步:拿查询窗口

会话接口要求 `from`/`to` 参数。先打开目标页,从它自己的请求里抄:

```bash
playwright-cli -s=msedge goto https://tsecbench.zc.tencent.com/agent/<RUN_ID>
sleep 6
playwright-cli -s=msedge requests | grep "llm/sessions"
```

grep 结果里的 `from=...&to=...` 就是该 run 的会话窗口(UTC,ISO 格式)。

## 第二步:拉数据

全部接口、字段说明见 [references/api.md](references/api.md)。取数模板:

```bash
playwright-cli -s=msedge --raw eval "() => fetch('<API 路径>',{headers:{authorization:'Bearer '+localStorage.getItem('token')}}).then(r=>r.text())" > out.json
```

注意:

- 带查询参数时把 `from`/`to` 原样拼进路径。
- 分页接口先看 `pagination.total_pages`,再循环拉全。
- eval 里的函数必须写成 `() => {...}` 箭头形式;`document.querySelector(...)` 之类
  语句形式会报语法错。

## 第三步:解码

eval 返回的是字符串,平台响应再包一层 JSON,因此**要剥两层**:

```python
import json
d = json.loads(json.loads(open('out.json', encoding='utf-8').read()))
```

漏剥一层时,典型报错是 `'str' object has no attribute 'get'`。

## 落盘位置

原始 JSON 存 `.tsecbench-analysis/`(仓库根,已 gitignore)。命名沿用:
`agent-<RUN_ID>.json`、`sessions[-<RUN_ID>].json`、`score-timeline[-<RUN_ID>].json`、
`leaderboard*.json`、编排器明细 `<run 缩写>-o<页码>.json`。

## Windows 坑

Git Bash 的 `/tmp` 与原生 Python 不互通。重定向目标一律用工作区内的
`.tsecbench-analysis/` 绝对路径。

## 拉完之后

若任务是分析(节奏、吞吐、瓶颈归因),先读
[references/analysis.md](references/analysis.md)——里面有已验证的口径:
响应级时间戳、斜率测试、思考占比、并发直方图。不要重新发明这些算法。
