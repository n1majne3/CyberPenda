# TSecBench 平台操作(上传镜像 / 发车 / 收尾 / 实时监控)

经验来源:2026-09 dispatch-p1..p3 三轮实际上传与发车。全部接口需
`authorization: Bearer <localStorage.token>`,且必须在 tsecbench 域名的
页面上下文里调用(localStorage 跨域隔离,GitHub/空白标签页取不到 token)。

## 会话与浏览器要点

- `playwright-cli attach --extension=msedge` 连的浏览器**不能 setFileInputFiles**
  (DOM.setFileInputFiles: Not allowed)——上传不要走文件选择器,一律走下面的 API。
- `/benchmark/running/<id>` 直播页事件流很重,在它上面跑 `eval` 会挂死;
  先 `tab-new` 开一个轻量页(首页)再操作。
- API 前端映射可逆向:`/assets/index-*.js` 主包 + 懒加载 chunk
  (如 `BenchmarkPrepareRouterView-*.js`),搜 `$.post(\`/runs` 一带能拿到
  完整 API 对象与参数形状。

## 跑分生命周期

| 状态 | 端点 | 说明 |
| --- | --- | --- |
| 进行中 | `GET /api/v1/runs/<id>/status` | 含 current_score;elapsed 在状态迁移时会重置,计时用挂钟 |
| 进行中 | `GET /api/v1/runs/<id>/score-timeline` | 实时得分事件 |
| 进行中 | `GET /api/v1/runs/<id>/llm/sessions?from=&to=&page=&page_size=50` | 直播期会话也可见(与终态的 leaderboard 端点不同) |
| 进行中 | `GET /api/v1/runs/<id>/llm/sessions/<sid>` | 单会话明细,分页 |
| 进行中 | `GET /api/v1/runs/<id>/llm/model-usage` | 实时 token 用量 |
| 收尾 | `POST /api/v1/runs/<id>/finish` | 对应 UI 的"结束评测",200 即受理 |
| 已终态 | `GET /api/v1/leaderboard/agent/<id>/...` | 跑分中返回 404,结束后才有 |

找自己的跑分(含进行中):`GET /api/v1/my/runs?page=&page_size=&set_id=&run_source=hosted`;
agent_id 从 `GET /api/v1/my/agents/options` 取(如 CyberPenda=1430)。

## 上传镜像(三步,免浏览器文件对话框)

1. **申请凭证**:`POST /api/v1/runs/hosted/upload-cred` body `{"filename":"xxx.tar.gz"}`
   → 返回腾讯 COS STS(tmp_secret_id/key、session_token、bucket、region、key、url,
   有效期约 30 分钟;2.3GB 实测 6MB/s 管道约 6 分钟,注意窗口)。
2. **签名直传**(COS XML API PUT,python 生成 Authorization):

```python
import hashlib, hmac, urllib.parse, json
c = json.loads(cred_json)
key_time = f"{c['start_time']};{c['expired_time']}"
host = f"{c['bucket']}.cos.{c['region']}.myqcloud.com"
pathname = '/' + c['key']
headers = {'host': host, 'x-cos-security-token': c['session_token']}
def q(s): return urllib.parse.quote(str(s), safe='')
hl = ';'.join(sorted(k.lower() for k in headers))
hstr = '&'.join(f"{k.lower()}={q(headers[k])}" for k in sorted(headers, key=str.lower))
http_string = f"put\n{pathname}\n\n{hstr}\n"
si = hashlib.sha1(http_string.encode()).hexdigest()
sts = f"sha1\n{key_time}\n{si}\n"
sign_key = hmac.new(c['tmp_secret_key'].encode(), key_time.encode(), hashlib.sha1).hexdigest()
sig = hmac.new(sign_key.encode(), sts.encode(), hashlib.sha1).hexdigest()
auth = (f"q-sign-algorithm=sha1&q-ak={c['tmp_secret_id']}&q-sign-time={key_time}"
        f"&q-key-time={key_time}&q-header-list={hl}&q-url-param-list=&q-signature={sig}")
# curl -X PUT -T 文件 -H "Authorization: <auth>" -H "x-cos-security-token: <token>" https://<host>/<key>
```

3. **登记**:`POST /api/v1/runs/hosted/upload-history`
   `{agent_id, file_name, app_dist: <完整 COS URL>, file_hash: <tar.gz 的 sha256>}`。
   app_dist 格式可用 `GET /api/v1/runs/hosted/upload-history?agent_id=` 反查历史确认。

上传大小上限 3GB(.tar/.tar.gz/.tgz);登记后 UI 的"最近使用版本"立即出现。

## 发车(创建跑分)

`POST /api/v1/runs`:

```json
{
  "set_id": "4",
  "agent_id": 1430,
  "run_mode": "full",
  "run_source": "hosted",
  "evaluation_type": "agent",
  "app_dist": "<COS URL>",
  "env_config": {"CYBERPENDA_RUNTIME": "pi", "...": "普通 KEY:VALUE 对象"},
  "remark": "可选"
}
```

坑:**set_id 必须是字符串**(传数字 4 返回 422 string_type);env_config 是扁平对象,
不是数组。201 返回 run_id;评测**次数有限**,4xx 时报告原文、不要盲目重试。
UI 表单路径也走得通(选 Agent → 添加变量逐行填 → 选镜像 → 勾协议 → 提交),
但"添加变量"行的 refs 行为不稳,API 路径更快。

提交大 JSON 的可靠方式:base64 编码 body,`playwright-cli run-code --filename`
里 `page.evaluate` 内 `atob` 解码后 fetch(避免 shell 转义地狱)。

## 高峰期经验

白天 API 速率与成功率显著差于夜间;正式跑分安排在 18:00 后
(用户 2026-09-22 指定)。验证性短跑(30 分钟)白天可用,收尾用 finish。
