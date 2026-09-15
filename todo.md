# PAIR 本地部署 todo

目标：在本机（macOS arm64）用**源码构建 + Electron 开发模式**跑起 NVIDIA Personal AI Router，
先只把 PAIR 本体跑通（服务起来、UI 能打开、本机节点可见），推理引擎（Ollama / LM Studio）稍后再装。

## 环境实况

| 项 | 要求 | 本机 | 状态 |
| --- | --- | --- | --- |
| OS / 架构 | macOS arm64 支持 | darwin 25.4.0 arm64 | ✅ |
| Go | ≥ 1.25 | 1.25.6 | ✅ |
| jq | 任意 | 1.7.1-apple | ✅ |
| Node | ≥ 25.5.0 | v24.18.0 (fnm 管理) | ❌ 需升级 |
| npm | 随 Node | 11.16.0 | — |
| 磁盘 | node_modules + Electron ≈ 1.5GB | 31GB 可用（已用 93%） | ⚠️ 够但不宽裕 |
| 推理引擎 | Ollama / LM Studio | 均未安装 | 本轮不装（按约定） |

## 已知风险

1. **GitHub 直连不通**（已实测确认）：`github.com:443` 直连 20s 超时，仅 `api.github.com`（gh CLI）可用。
   源码已通过 `gh api tarball` 获取。
2. **实测连通性结论**：

   | 源 | 结果 | 处置 |
   | --- | --- | --- |
   | registry.npmjs.org | ✅ 200 (3.0s) | 直连即可，npm 不用换源 |
   | github.com/electron releases | ❌ 超时 | 必须 `ELECTRON_MIRROR=https://npmmirror.com/mirrors/electron/` |
   | npmmirror 电子镜像 | ✅ 302 → cdn.npmmirror.com | 作为 Electron 源 |
   | proxy.golang.org | ❌ 超时 | 必须 `GOPROXY=https://goproxy.cn,direct` |
   | goproxy.cn | ✅ 200 (0.07s) | 作为 Go 源 |

   两个镜像变量只在命令行临时传入，**不写入全局 `go env -w` 和 `~/.npmrc`**。
3. **源码构建产物无签名、无自动更新**；升级靠重新拉源码重建。
4. **macOS 特权 helper 未知项**：release 版安装器会注册特权 helper 并加防火墙规则，
   开发模式下这步不做。所以「集群配对 / 跨机路由」等依赖入站端口的功能可能受限。
   本轮只验证单机跑通，配对能力留作后续观察。

## 待办

- [x] 0. 获取源码到 `/Users/xc-tech/code/Personal-AI-Router`（已通过 gh api tarball 完成，无 .git）
- [x] 1. 用 fnm 装 Node v25.9.0，项目根写 `.node-version` 固定版本
       —— 全局默认仍是 v24.18.0，未动 brew node
- [x] 2. 验证网络：npm ✅ 直连；Electron 需镜像；**新发现 Go proxy 也不通**，需 goproxy.cn
- [x] 3. `make tools` 工具链门禁全绿（go 1.25.6 / node 25.9.0 / npm 11.12.1 / jq 1.7.1）
- [x] 4. `make deps-go` 16 个 Go 模块依赖拉取 + 编译通过
- [x] 5. `make deps-node` 887 个 npm 包 23 秒装完，Electron v42.10.0 二进制就位
- [x] 6. `make build-binaries` 13 个 Go 服务二进制编译到 `desktop/cli-bin/`
- [x] 7. `make run` 启动成功：12 个服务进程 + Electron UI 窗口渲染完成，本机节点已被发现
- [x] 8. 记录实际启动命令与踩坑（见下方「审查」）

## 日常使用

```bash
cd /Users/xc-tech/code/Personal-AI-Router
export GOPROXY=https://goproxy.cn,direct
export ELECTRON_MIRROR=https://cdn.npmmirror.com/binaries/electron/
make run          # fnm 会按 .node-version 自动切到 v25.9.0
```

两个镜像变量在**重新装依赖**时才必需，日常 `make run` 不重新下载可以省略。
建议直接写进 shell profile 或用 direnv，免得每次手敲。

关掉：在 `make run` 的终端里 `Ctrl-C`，12 个子服务会被一起收掉。

无桌面环境时改用终端界面：`./services/build/bin/nvpair-tui`（需先 `cd services && ./build.sh`）。
**不要同时开桌面版和 TUI**，它们抢同一批服务和端口。

## 端点

| 端点 | 端口 | 用途 |
| --- | --- | --- |
| Ollama 兼容 | `http://127.0.0.1:11434` | `/api/tags`、`/v1/chat/completions` |
| LM Studio 兼容 | `http://127.0.0.1:1234` | `/v1/models`、`/v1/chat/completions` |
| 节点发现 | `14318` | 局域网节点互相发现 |
| Ollama 引擎实体 | `11435` | PAIR 托管的 Ollama 真身（未安装） |
| LM Studio 引擎实体 | `1235` | LM Studio 真身（未安装） |
| Vite dev server | `5173` | 仅开发模式，渲染进程热更新 |

注意 11434 是 **PAIR 的代理**，不是 Ollama 本身 —— 这正是它能冒充 Ollama 接管流量的原因。
将来自己装 Ollama 要避开 11434，或让 PAIR 改端口。

## 审查

### 结果

单机 PAIR 已跑通。实测状态：

- 12 个 Go 服务进程全部在跑（ui-broker / cluster-manager / engine-manager / job-scheduler /
  workload-manager / node-scanner / node-info / node-settings / manual-nodes / errors /
  ollama-proxy / lmstudio-proxy）
- Electron UI 窗口 394ms 渲染完成，Overview 页正常
- 本机节点已被自己发现：`XCdeMac-Studio.local`，UUID `812054ec-...`，
  暴露 4 个网段 IP（10.10.85.50 / 10.10.95.52 / 192.168.139.3 / 192.168.107.0）
- 三个代理端点都正常响应 HTTP，返回 `{"error":"model inventory unavailable"}`，
  服务端日志 `503 no valid model list from 0 candidate(s)`
  —— **这是预期正确行为**：路由器活着，只是没有引擎可路由。按约定本轮不装引擎。

### 踩的坑

1. **github.com:443 直连超时**（20s 无响应），`git clone` 完全不可用。
   绕法：`gh api repos/NVIDIA/Personal-AI-Router/tarball/main > par.tar.gz`，只有 api.github.com 通。
   **代价**：源码树没有 `.git`，将来更新只能重新下 tarball，不能 `git pull`。
2. **proxy.golang.org 也不通** —— 这是计划里没预料到的。`GOPROXY=https://goproxy.cn,direct` 解决。
3. **Electron 二进制**走 github releases，如预期卡住。
   `ELECTRON_MIRROR=https://cdn.npmmirror.com/binaries/electron/` 解决，23 秒拿到 v42.10.0。
4. **`make tools` 只 warning 不报错** —— Node 版本不够时它不会拦你，会一路跑到构建阶段才炸。
   所以 Node 必须先升级，别指望门禁救你。

### 遗留与未验证项

- **跨机配对未验证**。计划里标为未知项，现在依然未知：源码开发模式不注册 macOS 特权 helper、
  不加防火墙规则。节点发现服务在 14318 上已 LISTEN，但入站是否被 macOS 防火墙拦、
  PIN 配对能否走通，**没有第二台机器所以没测**。真要组集群，建议那台装官方 .dmg。
- **LM Studio 在线模型目录拉取失败**：`LM Studio catalog fetch failed: canceled (ERR_CANCELED)`，
  同一套网络限制。只影响在 UI 里浏览 LM Studio 云端模型列表，不影响路由本身。
- **引擎和模型没装**（按约定）。下一步想跑真推理，二选一：
  UI 里 Engine settings → Install Ollama，或自己 `brew install ollama`（注意端口冲突）。
- **磁盘**：项目占 887MB（其中 node_modules 776MB），剩余 29GB（94% 已用）。
  拉模型权重前先清磁盘 —— 一个 27B 量化模型就要 15-20GB。
- **构建产物无签名、无自动更新**，升级要重新下 tarball 重建。

### 对代码库的改动

只加了一个文件：`.node-version`（内容 `v25.9.0`），给 fnm 自动切版本用。
`todo.md` 是本次的工作记录。其余全是构建产物（`desktop/node_modules/`、`desktop/cli-bin/`、
`desktop/resources/icons/`），都在上游 `.gitignore` 里，未改动任何上游源码。

## 不做的事

- 不下载官方 .dmg，不往 `/Applications` 安装（按你的选择走源码）
- 不装 Ollama / LM Studio，不拉任何模型权重
- 不改动全局 Node（不 `brew install node@25`，不改默认 fnm 版本）
- 不做可分发打包（`build:mac:arm64` / electron-builder），只跑开发模式
