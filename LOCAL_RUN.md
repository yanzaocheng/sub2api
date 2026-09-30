# 本地运行

访问 http://127.0.0.1:8080 。前端已嵌入 `.dev/sub2api.exe`，本地直接运行程序，不需要 Docker。

2026-10-01 已同步上游 v0.2.11（`42bc7f6cf`），重新构建前后端，并保留本地 Claude Code / Bun 1.4.3 TLS 指纹修改。旧 Git 合并已完成。更新前的源码和 Git 状态备份位于 `.dev/update-backup-20261001-020907`，旧程序保留为 `.dev/sub2api-0.2.10-backup.exe`。

## 定制分支

`custom` 保存你的定制功能、TLS 指纹修改和本地启动脚本；`main` 保持官方代码基线，官方远程仓库仍叫 `origin`。通常留在 `custom` 分支上开发。

同步官方更新：

```powershell
git switch custom
git status
# 先提交本次完成的定制，或用 git stash 暂存未完成的修改。
git fetch origin --tags
git merge origin/main
# 若出现冲突，保留定制并合并上游改动；检查后提交合并结果。
# 重新构建、测试并启动本地服务。
```

可以为每个功能创建分支，例如 `git switch -c feature/custom-billing custom`，完成后合并回 `custom`。

个人远程 `personal` 指向 https://github.com/yanzaocheng/sub2api ，定制代码保存在 `personal/custom`。`custom` 的默认推送目标是个人仓库，官方远程 `origin` 用于获取更新。提交完成的源码后，可运行 `git push personal custom` 备份定制代码。连接配置、私钥和 `.dev/` 不应加入提交。

在项目目录执行：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\start-local.ps1
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\stop-local.ps1
```

启动脚本复用正在运行的后端，检查健康接口和公开设置接口，并在后台运行 SSH 隧道监护进程。隧道断开后会自动重连。首次启动可能需要约两分钟。

已验证首页、登录页、6 个入口资源、健康接口和公开设置接口返回 200，PostgreSQL 只读查询及 Redis 认证 PING 成功。主动断开本地隧道后，监护进程约 9 秒内恢复连接及页面接口。

数据库和 Redis 通过 SSH 连接服务器现有实例，分别映射到本机 `127.0.0.1:15432` 和 `127.0.0.1:16379`。本地后台与服务器共用数据，管理页面保存的修改也会影响服务器数据。登录使用服务器现有账号。

私密连接配置、程序和日志保存在 Git 忽略的 `.dev/` 中。启动依赖本机 Python、`psutil`、Windows OpenSSH，以及 `.dev/ssh-tunnel.config` 指定的现有私钥。日志为 `.dev/backend.stdout.log`、`.dev/backend.stderr.log`、`.dev/tunnel.stderr.log` 和 `.dev/supervisor.stdout.log`。

本次启动前核对了 289 个 SQL 迁移，全部与服务器文件名和校验值一致，没有待执行的新迁移。本地配置关闭了令牌自动刷新、仪表盘聚合和用量清理任务。

本次前端在 `.dev/frontend-build` 中安装依赖和构建，页面产物位于 `backend/internal/web/dist`。源文件仍位于 `frontend/`，修改后需重新同步并构建页面，再以 `go build -tags=embed ./cmd/server` 构建后端。服务运行时先构建至另一个文件，停止本地后端后再替换 `.dev/sub2api.exe`。

SSH MCP `ssh-poly` 已配置；`execute_command` 的自动批准设置写入用户级 Codex 配置。重新打开会话后加载新设置。
