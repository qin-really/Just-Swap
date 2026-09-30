# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 项目

**JustSwap** — 局域网文件传输工具。一台设备跑服务器,任何浏览器访问,上传/浏览/下载。纯 HTTP,内嵌 UI,单二进制。

## 构建与检查

```bash
gofmt -l .              # 无输出 = 已格式化
go vet ./...            # 无输出 = 通过
go build -o js.exe .    # 单二进制
go test ./... -count=1
```

**当前仓库没有任何 `*_test.go`**,`go test` 六个包全部输出 "no test files"。README「校验」一节已如实说明;新增测试时以 `httptest` 起真路由为主。

零依赖。`go.mod` 只有 `module justswap` + `go 1.22`。页面二维码由前端 `internal/webui/qrcode.min.js` 生成(第三方 JS,MIT,作为静态资源 `//go:embed` 进二进制,Go 侧无第三方包)。

## 架构

```
main.go                      参数解析,把已存设置灌进 flag 默认值
internal/app/app.go          装配 + 进程生命周期(启动 Wipe、housekeeping、端口探测、优雅关闭)
internal/app/settings.go     设置读写(UserConfigDir/justswap/settings.json)
internal/core/core.go        集中状态:内存索引 map + 一把 mutex
internal/core/events.go      事件总线:SSE 推送,非阻塞
internal/store/store.go      磁盘:文件名清洗、.part 流式写、碰撞改名、目录清理
internal/server/server.go    路由注册(Go 1.22 method pattern)
internal/server/api.go       所有 HTTP handler
internal/webui/webui.go      //go:embed index.html qrcode.min.js
internal/webui/index.html    单页中文 UI(内联 CSS/JS + 二维码)
internal/webui/qrcode.min.js 前端二维码库(qrcode-generator,MIT,静态 embed)
```

**约定:一个集中状态 + 一把 mutex,所有变更经 core。** handler 不直接摸磁盘。事件在解锁**之后**发布,避免重入 core。

**分层方向不要反:** store 只知道字节,core 只知道索引,谁都不知道对方。`housekeep` 因此放在 app 层——它需要同时调用 core 的 `PurgeExpired` 和 store 的 `Remove`,这个位置是唯一不让任何一层向下越界的地方。

## 非显然细节

1. **`embed.FS` 不是 `http.Handler`。** 必须用 `http.FileServerFS(webui.FS)`。直接传会编译报错。

2. **`ExpiresIn` 是 `int`(秒),不是 `time.Duration`。** `time.Duration` 序列化为纳秒,前端会算错。过期是动态判定 `now - UploadedAt > retention`,不存 `expiresAt`——所以改 TTL 立刻影响所有文件。

3. **过期文件不会从列表消失。** `Files()` 对已过期条目返回 `ExpiresIn: 0` 而不是隐藏它;是 housekeeping(30s 一次)去删字节。UI 需要展示「已过期」,不能让它凭空蒸发。

4. **文件名分隔符是剥离不是拒绝。** `photos/img.png` → `photosimg.png`(平铺)。但穿越在剥离**之前**拒绝:`../../etc/passwd` → 400,不会变成无害的 `..etcpasswd`。以 `/` 或 `\` 开头拒绝;含 `:` 拒绝(Windows ADS);含 NUL 拒绝;尾部 `.` 被 trim;长度上限 `MaxNameBytes = 200`。

5. **元数据不持久化。** 文件名/大小/上传时间只在内存。所以启动时 `st.Wipe()` 清孤儿——这是「元数据只在内存」这条设计的必要代价,不是可选清理。**设置存磁盘**,两者分开。

6. **`ReadTimeout`/`WriteTimeout` 保持 0。** SSE 和流式上传/下载是长连接,加了会静默掐断。实际生效的是 `ReadHeaderTimeout 10s`、`IdleTimeout 120s`、`MaxHeaderBytes 1<<20`。

7. **关闭清空默认开,但必须是显眼可关的开关。** UI 顶部有 `.notice` 徽标「开放访问 · 无认证」,启动 banner 也打印无认证警告。`cleanupOnExit` 无论开关如何都记录一行。

8. **`/api/meta` 的 `alias` 目前是恒空字段。** `app.go` 构造 `core.Config` 时漏传了 `Alias`,而 UI 也不渲染它——所以 `-alias` 参数、`defaultAlias()`、`Settings.Alias` 三处全是死路。动 alias 相关代码前先确认这一点。

9. **`OnConfig` 会把 `cfg.BaseDir` 写回 settings.json**,这与它上方注释「flags set at launch are never written back」的意图矛盾:一次性 `--download-dir ./x` 会被持久化成偏好。改持久化行为时注意这处冲突。

10. **端口会被自动递增。** `findAvailablePort` 从 `-port` 起最多试 10 个,占用了就换。用户指定的端口可能不是实际端口,banner 和 `/api/meta` 之外不要假设端口。

11. **`displayHost` 用 UDP dial 探测局域网 IP**,不发包,所以断网可用;探测失败退回 `127.0.0.1`。`listen=127.*` 时直接返回 localhost。

12. **`-race` 在 Windows 上无法链接**(`gotsan.cpp` 缺少 `WakeByAddressSingle`)。环境问题,不是代码信号。

## 安全边界

- 开放无认证是明确选择。UI 顶部有可见警告,banner 也打印。
- 路径穿越:下载/删除的 `name` 一律在 `store` 里重新清洗校验,不信任客户端返回值。**store 的每个公开方法都自己做校验**——这是最后一道防线。
- JSON body 上限 4MiB(`MaxBodyBytes`),只限 JSON,不限文件。文件走 `.part` 流式写,中途中断不留下「看起来完整」的半截文件。
- 碰撞自动改名 `name (1).ext`;上传返回最终名,客户端一律用返回值,不要自己拼。
- SSE 总线 `Publish` 永不阻塞:订阅者缓冲(256)满就丢事件。这是有意取舍——列表会重连重拉,而发布者被阻塞不可恢复。

## API

| 方法 | 路径 | 响应 |
|---|---|---|
| GET | `/api/meta` | `product`、`version`、`alias`、`baseDir`、`retentionSeconds`、`clearOnShutdown`、`fileCount`、`local` |

`local` 为 true 表示请求来自本机(loopback 或 `localIPs()` 枚举的接口 IP),UI 据此让二维码默认只在部署机显示、其他设备默认隐藏(可手动开关)。二维码本身由前端从 `location.origin` 拼内容,后端不知道 URL,端口变化(自动递增)天然正确。
| GET | `/api/files` | `{files: [...]}`,按上传时间倒序,每项带 `expiresIn` |
| POST | `/api/upload?name=` | 201 `{name, bytes, url}`;`name` 缺失或非法 → 400 |
| GET | `/api/download?name=` | 流式下载,`Content-Disposition`(ASCII + `filename*=UTF-8''`);支持单段 Range / 206 / `Content-Range`,含后缀范围 `bytes=-N`;404 仅当文件不存在,名字非法 → 400 |
| DELETE | `/api/files?name=` | 204;store 的 Remove 幂等,文件已不在不算错 |
| POST | `/api/clear` | `{removed: n}`;先删字节再清索引 |
| PATCH | `/api/settings` | 指针字段,只改传入项;`retentionSeconds` 下限 1s、上限 366 天 |
| GET | `/api/events` | SSE:`hello`(meta)→ `files`(全量)→ 后续增量;25s `:ping` |

路由用 Go 1.22 的 method pattern(`mux.Handle("GET /api/meta", …)`),这就是 `go.mod` 要 1.22 的原因。

## 测试

目前无测试文件。按优先级建议补:

- `internal/store/store_test.go` — SafeName 各拒绝分支、碰撞改名、Wipe、Remove 幂等
- `internal/core/core_test.go` — 排序、过期、TTL 动态、Bus 非阻塞
- `internal/server/server_test.go` — httptest 起真路由:上传→列表→下载 sha 一致、Range 206、删除、清空、设置 PATCH、SSE files 事件、路径穿越一律 400

## 约定

- 代码注释和标识符用英文。UI 文案用中文。
- 中文回复、中文 Git 提交信息。
- 技术术语(API、SDK 等)可保留英文。
- 行尾统一 LF。仓库当前只有 `internal/app/app.go` 是 CRLF,导致 `gofmt -l .` 报错——`gofmt -w internal/app/app.go` 可修。

## Agent skills

### Issue tracker

Issue 以 Markdown 文件存放在 `.scratch/<feature-slug>/`。见 `docs/agents/issue-tracker.md`。

### Domain docs

见 `docs/agents/domain.md`。**当前仓库既没有根目录 `CONTEXT.md` 也没有 `docs/adr/`**——按该文档的规定,静默继续即可,不要主动创建。
