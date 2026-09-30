# 默认 TLS 指纹修正（2026-10-01）

已修改 `backend/internal/pkg/tlsfingerprint/dialer.go` 的内置默认参数，使其与本次提供的 Claude Code / Bun 1.4.3 抓取结果一致。

| 实际 ClientHello 抓取用例 | JA3 | JA4 |
| --- | --- | --- |
| nil Profile | `1523504b38f0fae0d881d4b6554aac1b` | `t13d1713h1_5b57614c22b0_6a3d802a7139` |
| 空 Profile | `1523504b38f0fae0d881d4b6554aac1b` | `t13d1713h1_5b57614c22b0_6a3d802a7139` |

默认扩展移除 ECH（65037），supported_groups 使用 `[4588,29,23,24]`，key_share 使用 `[4588,29]`。真实 key share 长度分别为 1216 和 32 字节；ALPN 仅为 `http/1.1`，保留 17 个加密套件和 13 个扩展，不添加 GREASE。

`dialer_wire_test.go` 直接调用真实 `DialTLSContext`，通过 `net.Pipe` 读取输出字节并独立解析和计算 JA3、JA4，验证的是实际发出的首次 ClientHello。旧自定义 Profile 显式设置传统曲线时仍默认使用 X25519 key share。

验证通过：TLS 包的 unit/short 测试（7 个本地测试，另有 3 个外网测试跳过），集成标签下的 `TestBuildClientHelloSpecNewFields`，相关合并字段的仓库、服务、DTO 和管理处理器测试，以及前后端构建。

此报告覆盖首次 ClientHello，不代表整个 Claude Code 的 HTTP 请求行为完全一致。账户需要启用 TLS 指纹；绑定的自定义 Profile 会覆盖内置参数。TLS 解密代理会替换上游指纹，普通 CONNECT/SOCKS 隧道不会自行替换 ClientHello。

之前的 `tls-dialer-verification.md` 和 JSON 为修改前的历史记录。
