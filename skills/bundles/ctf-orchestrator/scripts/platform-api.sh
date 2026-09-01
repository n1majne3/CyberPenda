#!/usr/bin/env bash
# 平台适配层示例：接口签名固定，换环境只改 BASE/鉴权头/endpoint 路径。
# usage: platform.sh list | start <code> | close <code> | hint <code> | submit <code> <flag>
# 凭证从环境变量读取（按目标平台调整变量名）。
set -u
: "${PLATFORM_TOKEN:?export PLATFORM_TOKEN=... 先设置令牌}"
: "${PLATFORM_BASE_URL:?export PLATFORM_BASE_URL=https://... 先设置平台地址}"
BASE="${PLATFORM_BASE_URL%%/}"
TOKEN="$PLATFORM_TOKEN"
AUTH="Authorization: Bearer $TOKEN"   # ← 按平台改鉴权头格式；TsecBench 系为 BENCHMARK_TOKEN: $TOKEN

cmd="${1:-}"
case "$cmd" in
  list)    # ← 题目列表
    curl -s -m 30 -H "$AUTH" "$BASE/openapi/v1/challenges"
    ;;
  start)   # ← 申请资源，返回地址
    code="${2:?usage: start <code>}"
    curl -s -m 60 -X POST -H "$AUTH" "$BASE/openapi/v1/challenges/start?unique_code=$code"
    ;;
  close)   # ← 释放资源
    code="${2:?usage: close <code>}"
    curl -s -m 60 -X POST -H "$AUTH" "$BASE/openapi/v1/challenges/close?unique_code=$code"
    ;;
  hint)    # ← 查看提示（如有）
    code="${2:?usage: hint <code>}"
    curl -s -m 30 -H "$AUTH" "$BASE/openapi/v1/challenges/hint?unique_code=$code"
    ;;
  submit)  # ← 提交 flag
    code="${2:?usage: submit <code> <flag>}"
    flag="${3:?usage: submit <code> <flag>}"
    curl -s -m 30 -X POST -H "$AUTH" -H "Content-Type: application/json" \
      -d "{\"unique_code\":\"$code\",\"flag\":$(python3 -c 'import json,sys;print(json.dumps(sys.argv[1]))' "$flag")}" \
      "$BASE/openapi/v1/challenges/submit"
    ;;
  *)
    echo "unknown cmd: $cmd" >&2; exit 1
    ;;
esac
