# net-capture —— 看穿网页到底在发什么请求

两条路，任选：

| | 路线 A：CDP 直控（推荐） | 路线 B：手动导出 HAR |
|---|---|---|
| 谁启动浏览器 | 你跑一次 `launch-chrome.ps1`，之后我全程驱动 | 你自己开 DevTools |
| 我能做什么 | 开标签、导航、执行 JS、**实时录制全量请求+响应体** | 只能分析你导出的那份 |
| 需要登录 | 首次要（独立 profile） | 不用（用你现有登录） |
| 装东西 | 不用 | 不用 |

两条路最终都产出**标准 HAR 1.2**，喂给同一个分析器。

---

## 路线 A：CDP 直控

### 1. 你在自己的终端里启动浏览器（只需一次）

```powershell
cd D:\d\sub2api\tools\net-capture
.\launch-chrome.ps1
```

它做的事：用**独立的 profile 目录**（`.chrome-profile\`）启动一个带
`--remote-debugging-port=9222` 的 Chrome，并挂上你本机的 Clash 代理
`http://127.0.0.1:7892`。

- 独立 profile = 不碰你现在的浏览器会话
- Chrome 136+ 用默认 profile 时会**忽略**调试端口，所以必须独立 profile
- 首次运行要在那个窗口里登录一次 claude.ai；之后登录态留在 profile 里
- 用 TUN 模式不想挂代理？`.\launch-chrome.ps1 -NoProxy`
- 端口冲突？`.\launch-chrome.ps1 -Port 9333`

### 2. 我接管

```powershell
node cdp.mjs status        # CDP 通不通、浏览器版本
node cdp.mjs tabs          # 有哪些标签
node cdp.mjs nav https://claude.ai/
node cdp.mjs eval "document.title"

# 录一段流量（默认只录 claude.ai 那个标签，60 秒）
node cdp.mjs capture --seconds 90 --out reports\claude.har

# 抓某条具体请求的完整请求/响应
node cdp.mjs raw "current_user_access"
```

`capture` 结束后接着分析：

```powershell
node analyze-har.mjs reports\claude.har --md reports\claude.md
```

**录制期间你可以正常操作页面** —— 打开会话、发消息、切设置，全都录得到。

### capture 参数

| 参数 | 默认 | 说明 |
|---|---|---|
| `--seconds N` | 60 | 录多久（Ctrl+C 可提前结束，照样落盘） |
| `--out <file>` | `reports\cdp-<ts>.har` | 输出路径 |
| `--match <substr>` | `claude.ai` | 附着到哪个标签 |
| `--max-body N` | 524288 | 单个响应体截断上限（字节） |
| `--all` | 关 | 连大图片/字体也录（默认只丢无参数的静态资源） |

---

## 路线 B：手动导出 HAR

1. DevTools（`F12`）→ **Network** 面板 → 勾上 **Preserve log**
2. 刷新页面，或把要分析的操作走一遍
3. 请求列表空白处 **右键 → Save all as HAR with content**
   （中文：**全部保存为 HAR（含内容）**）
4. 存到本目录，然后：

```powershell
.\run.ps1                    # 自动挑最新的 .har
.\run.ps1 claude.har         # 指定文件
```

> ⚠️ HAR **明文包含你的 Cookie 和 Authorization**。分析器默认打码后再输出，
> 但**原文件本身是明文**，不要外发。只想给一部分就先按 `Domain:claude.ai` 过滤再导出。

---

## 分析器

```powershell
node analyze-har.mjs <file.har> [--md out.md] [--json out.json] [--host H] [--no-redact]
```

报告包含：

1. **Host 排行** —— 一眼看出哪些流量根本不属于这个站点
2. **资源类型分布**
3. **第三方遥测 / 统计** —— 按服务名归类（Datadog RUM、Sentry、GA、统计像素…）
4. **接口端点表** —— xhr/fetch 归并；UUID / 纯数字 ID / 长 token 归一化成 `{uuid}` `{n}` `{tok}`
5. **失败的请求** —— 附真实错误码（`net::ERR_*`）
6. **端点细节** —— 实际发出的请求头、POST body 顶层字段名
7. **不透明标识符** —— 查询串里的长随机串，逐个追来源
8. **静态资源清单**

---

## 为什么需要你手动启动浏览器？

当前 agent 沙箱里 **Chrome 起不来**。实测：

- Chrome 进程能创建，但**立刻退出**，profile 只写了一半，调试端口没监听
- Node 的 `child_process.spawn` 报 `EPERM`
- 原因是沙箱禁止**命名管道**，而 Chrome 在 Windows 上的多进程 IPC 正是走命名管道
- 但 **loopback TCP 是通的**（实测 `OUTBOUND_LOOPBACK: OPEN`）

所以：我起不了浏览器，但**只要你把 CDP 端口开出来，我就能完全驱动它**。

---

## 自测（无浏览器也能跑）

`_mock-cdp.mjs` 是一个最小假 CDP 端点，用来验证协议管路：

```powershell
node _mock-cdp.mjs 19222            # 一个终端
$env:CDP_URL = "http://127.0.0.1:19222"
node cdp.mjs status
node cdp.mjs capture --seconds 3 --out reports\_mock.har
node analyze-har.mjs reports\_mock.har --md reports\_mock.md
```

`reports\_mock.md` 是它跑出来的样子。
`_fixture.har` + `reports\_fixture.md` 是手动 HAR 路线的样本。

---

## 脚本说明

| 文件 | 作用 |
|---|---|
| `launch-chrome.ps1` | **你运行**。启动带 CDP 的独立 Chrome |
| `cdp.mjs` | **agent 运行**。CDP 客户端：status / tabs / nav / eval / capture / raw |
| `analyze-har.mjs` | HAR → Markdown/JSON 报告 |
| `run.ps1` | 手动 HAR 路线的一键封装 |
| `_mock-cdp.mjs` | 假 CDP 端点，用于无浏览器自测 |
| `_probe-*.mjs` | 沙箱能力探测（loopback / spawn） |

> `.ps1` 全部**只用 ASCII**。Windows PowerShell 5.1 对不带 BOM 的 `.ps1`
> 按 ANSI 解码，中文注释会让它解析失败。Node 脚本不受影响，输出中文正常。
