# CLAUDE.md

## 项目

**JustSwap** — 局域网文件传输工具。一台设备跑服务器,任何浏览器访问,上传/浏览/下载。纯 HTTP,纯 stdlib,单二进制,内嵌 UI。

## 构建与测试

```bash
gofmt -l .              # 无输出 = 已格式化
go vet ./...            # 无输出 = 通过
go build -o js.exe .    # 单二进制
go test ./... -count=1  # 集成测试,用 httptest 起真路由
```

无外部依赖。`go.mod` 只有 `module justswap` + `go 1.22`。

## 架构

```
main.go                      参数解析、信号、启动
internal/app/app.go          装配 + 进程生命周期(启动 Wipe、housekeeping、优雅关闭)
internal/app/settings.go     设置读写(UserConfigDir/justswap/settings.json)
internal/core/core.go        集中状态:内存索引 map + 一把 mutex
internal/core/events.go      事件总线:SSE 推送
internal/store/store.go      磁盘:文件名清洗、.part 流式写、碰撞改名、目录清理
internal/server/server.go    路由注册
internal/server/api.go       所有 HTTP handler
internal/webui/webui.go      //go:embed index.html
internal/webui/index.html    单页中文 UI
```

**约定:一个集中状态 + 一把 mutex,所有变更经 core。** 不在 handler 里直接摸磁盘。事件在解锁后发布,避免重入。

## 非显然细节

1. **`embed.FS` 不是 `http.Handler`。** 必须用 `http.FileServerFS(webui.FS)`。直接传 `embed.FS` 会编译报错。

2. **`ExpiresIn` 是 `int`(秒),不是 `time.Duration`。** `time.Duration` 序列化为纳秒,前端会算错。改 TTL 立即影响所有文件(动态判定 `now - UploadedAt > retention`),不存 `expiresAt`。

3. **文件名分隔符是剥离不是拒绝。** `photos/img.png` → `photosimg.png`(平铺)。但穿越在剥离**之前**拒绝:`../../etc/passwd` → 400,不是变成无害的 `..etcpasswd`。以 `/` 或 `\` 开头也拒绝。含 `:` 拒绝(Windows ADS)。

4. **元数据不持久化。** 文件名/大小/上传时间只在内存。重启后无法列出遗留文件,所以启动时 `st.Wipe()` 清孤儿,关闭时也清(可关)。**设置存磁盘**,文件元数据不存。

5. **`ReadTimeout`/`WriteTimeout` 保持 0。** SSE 和流式上传/下载是长连接,加了会静默掐断。`ReadHeaderTimeout 10s`、`IdleTimeout 120s`。

6. **关闭清空默认开,但必须是显眼可关的开关。** UI 顶部有可见警告("开放访问·无认证")。启动日志也打印无认证警告。`cleanupOnExit` 无论如何都记录一行。

7. **`-race` 在 Windows 上无法链接**(`gotsan.cpp` 缺少 `WakeByAddressSingle`)。这是环境问题,不是代码信号。

## 安全边界

- 开放无认证是明确选择。UI 顶部有可见警告。
- 路径穿越:下载/删除的 `name` 一律重新清洗校验,不信任客户端返回值。
- JSON body 上限 4MiB,只限 JSON,不限文件。文件流式写 `.part`。
- 碰撞自动改名 `name (1).ext`。
- 上传返回最终名,客户端用返回值做后续操作(不要自己拼)。

## API

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/meta` | version、alias、baseDir、retentionSeconds、clearOnShutdown、fileCount |
| GET | `/api/files` | 文件列表(按上传时间倒序,带 `expiresIn`) |
| POST | `/api/upload?name=` | 流式上传,清洗 + 碰撞改名,返回最终名 + size |
| GET | `/api/download?name=` | 流式下载,支持 Range/206/Content-Range,Content-Disposition |
| DELETE | `/api/files?name=` | 删除 |
| POST | `/api/clear` | 清空全部 |
| PATCH | `/api/settings` | 更新 retentionSeconds / clearOnShutdown,持久化 |
| GET | `/api/events` | SSE:hello、files、settings,25s ping |

## 测试

- `internal/store/store_test.go` — SafeName 穿越拒绝、碰撞改名、Wipe、Remove 幂等
- `internal/core/core_test.go` — 排序、过期、TTL 动态、OnConfig、Bus 非阻塞
- `internal/server/server_test.go` — httptest 起真路由:上传→列表→下载 sha 一致、Range 206、删除、清空、TTL 清理、设置 PATCH、SSE files 事件、路径穿越一律 400
- `internal/app/app_test.go` — cleanupOnExit 行为、默认设置

## 约定

- 代码注释和标识符用英文。UI 文案用中文。
- 中文回复、中文 Git 提交信息。
- 技术术语(API、SDK 等)可保留英文。
