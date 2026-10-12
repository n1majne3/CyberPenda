# 区分 Sandbox Browser 和 Host Browser

## 状态

已接受,2026-10-12。工具映射、Task Launch 显式启用和直接 MCP 桥接边界已确认。独立 Sandbox 容器到 Edge 的桥接已验证。Task Launch 接入已实现。

## 背景

Runtime 需要区分独立浏览器会话与用户已有的真实浏览器会话。
Browser Profile 是浏览器状态,不是 Runtime Profile。
宿主机 Playwright MCP 的能力不全部在浏览器页面内执行。例如,`browser_run_code_unsafe` 在宿主机 MCP 服务进程中执行调用方提交的 JavaScript。
操作员接受此边界,要求在 Task Launch 时显式启用 Host Browser,不引入新的网关模块。
此决定取代桥接验证阶段提出的新增网关建议。

## 决定

- **Sandbox Browser** 使用 Sandbox 内的 `agent-browser`,不配置持久 Browser Profile。
- **Host Browser** 使用 Playwright MCP 的 extension 模式,连接宿主机真实 Browser Profile 中的现有会话。
- Host Browser 通过浏览器连接复用会话,不是把宿主机 Browser Profile 挂载或复制到 Sandbox。
- 浏览器选择与 Runner 选择独立。使用 Host Browser 不表示执行 Host Runner Activation。
- **Host Browser Enablement** 是 Task 或 Non-Project Session 启动时的显式选择,默认关闭。任务目标、Skill 或 Runtime Profile 中的 MCP 配置不替代此选择。
- 启用入口说明真实 Browser Profile 的访问能力和宿主机 MCP 服务进程的代码执行能力。操作员启用即接受这两类能力,不将 `browser_run_code_unsafe` 的过滤作为接入前提。
- 启用选择记录在该 Runtime Owner 的 Runtime Configuration Snapshot 中。关闭时不投影 CyberPenda 管理的 Host Browser MCP 配置;启用时通过现有 Config Projection 和 MCP 配置投影路径提供连接。
- Sandbox 内的 Runtime 直接通过 HTTP MCP 连接宿主机 Playwright MCP extension 服务。不新增网关模块,不新增网关承担的工具过滤或 tab 隔离层。

## 结果

Runtime 的工具说明必须明确标识 Sandbox Browser 和 Host Browser。
Host Browser Enablement 控制 CyberPenda 是否为该 Runtime Owner 提供受管理的宿主机浏览器能力,不是宿主机网络防火墙或 MCP 服务进程的隔离机制。
启用后,Runtime 仍可位于 Sandbox,但经宿主机 MCP 发起的操作由该服务的宿主机进程权限决定。此方案不承诺独立的 tab 或账号隔离。
浏览器连接本身不提供 Project Scope 授权。

## 验证与实施状态

独立只读 Sandbox 容器已通过宿主机 loopback HTTP MCP 连接现有 Edge 扩展。在连接可见的 Edge 会话中新建测试 tab,通过固定 Playwright 代码读取测试标记、取得页面快照并点击测试按钮。相同 tab 的计数从 `0` 变成 `1`,宿主机测试服务独立确认收到相同标记和计数 `1`。

本次没有验证业务账号的登录状态,也没有完成三个独立 `browser_evaluate`、`browser_snapshot`、`browser_click` 调用的成功验收。验证使用了 `browser_run_code_unsafe` 中的固定测试代码。

产品接入提供 Task Launch 开关、启动前依赖检查、Task 级 MCP 服务和 Codex、Claude Code、Pi 的原生 MCP 配置投影。Resume 保留 Snapshot 中的启用选择,并启动新的 MCP 服务。Stop、正常结束、启动失败和 daemon shutdown 关闭受管理的 MCP 进程,不关闭用户的浏览器。生命周期测试使用独立假进程,不操作真实 Browser Profile。完整 CyberPenda Task 到真实账号的验收尚未完成。

## 配置与使用

1. 在运行 daemon 的宿主机安装 Edge 或 Chrome,并安装 Playwright MCP 浏览器扩展。验证版本为 `@playwright/mcp@0.0.79`。可用 `npm install -g @playwright/mcp@0.0.79` 安装服务。
2. 确保 `playwright-mcp` 位于 daemon 的 PATH。也可用 `-host-browser-mcp-binary` 或 `PENTEST_HOST_BROWSER_MCP_BINARY` 指定二进制路径。
3. 默认浏览器为 Edge (`msedge`)。Chrome 使用 `-host-browser-browser chrome` 或 `PENTEST_HOST_BROWSER_BROWSER=chrome`。非标准安装位置使用 `-host-browser-executable` 或 `PENTEST_HOST_BROWSER_EXECUTABLE`。不会自动安装浏览器或扫描、复制用户 Profile。
4. 在 Task Launch 或 New Session 勾选 `Enable Host Browser`。API 使用 `host_browser_enabled: true`。Runtime Profile 不能通过受管理的 `host_browser` MCP 名称隐式启用。任意外部 MCP 和自定义原生配置不属于此开关的网络隔离边界。
5. 首次工具调用时,在浏览器扩展中批准连接。daemon 不继承 `PLAYWRIGHT_MCP_EXTENSION_TOKEN`,也不跳过扩展授权。

MCP 服务只监听宿主机 `127.0.0.1` 的临时端口。Host Runner 使用 loopback URL;Sandbox Runner 使用 `host.docker.internal` URL。Sandbox 引擎必须支持从容器访问宿主机 loopback 服务。macOS OrbStack 已验证;Linux Docker 的 host-gateway 通常不能访问 loopback,不保证可用。Preflight 检查本地二进制和浏览器安装,不启动浏览器、不确认扩展连接或容器网络可达性。

服务日志保存在 Task Runtime 根目录的 `host-browser/service.log`。新 Continuation 使用当前受管理服务的端口,不复用已停止的端口。服务意外退出时,工具连接失败;本实现不自动重启服务。Task 结束后,用户的 tab 和账号状态仍由浏览器持有。本实现不承诺清理这些状态。

Non-Project Session 使用同一 MCP 服务启动和配置投影路径。Session Preflight 只检查显式启用的依赖,不启动服务。Session Snapshot 保存选择,后续消息恢复 Runtime 时保留该选择。服务日志位于 Session Workdir 的 `.runtime/host-browser/service.log`。启动失败、Runtime 退出、Stop 和 daemon shutdown 清理服务,不关闭用户浏览器。Session HTTP 回归测试使用假 MCP 和 Runtime 进程,覆盖默认关闭、显式启动、Resume、Stop 和 daemon shutdown;尚未执行真实浏览器的 Session 验收。
