# JustSwap

局域网文件传输工具。一台设备跑服务器,任何浏览器访问,上传、浏览、下载。

纯 Go 标准库,零依赖,单二进制,内嵌网页 UI。

## 功能

- **上传**:拖拽或选择文件,实时进度
- **浏览**:文件列表,按上传时间倒序,显示剩余保留时间
- **下载**:支持断点续传(Range / 206 / Content-Range)
- **设置**:网页端可调保留时长(1 分钟 ~ 7 天)、关闭时是否清空文件
- **即用即走**:文件默认在保留期后自动删除,关闭服务器时清空(可关)

## 构建

```bash
go build -o justswap .
```

需要 Go 1.22+。编译产物为单二进制,无外部依赖。

## 使用

```bash
justswap
```

400ms 后自动打开浏览器(`-no-browser` 可关)。上传的文件存到 `-download-dir`,默认当前目录下的 `data/`,随二进制走。

### 命令行参数

| 参数 | 默认值 | 说明 |
|---|---|---|
| `-port` | `8787` | 监听端口 |
| `-listen` | `0.0.0.0` | 监听地址 |
| `-alias` | 见下 | 浏览器显示的服务器名称 |
| `-download-dir` | 上次保存值,首次 `data` | 文件存储目录(相对路径,随二进制走) |
| `-retention` | 上次保存值,首次 `3600` | 文件保留秒数 |
| `-clear-on-shutdown` | 上次保存值,首次 `true` | 关闭服务器时清空文件 |
| `-no-browser` | `false` | 启动时不打开浏览器 |
| `-version` | — | 显示版本并退出 |

`-alias` 的取值顺序:上次保存的值 → 主机名 → 当前用户名 → `JustSwap`。

### 设置持久化

设置单独存在系统配置目录:`%AppData%/justswap/settings.json`(Windows)、`~/.config/justswap/settings.json`(Linux)、`~/Library/Application Support/justswap/settings.json`(macOS)。字段:`alias`、`baseDir`、`retentionSeconds`、`clearOnShutdown`。

网页上改设置会写回这里,下次启动作为命令行参数默认值生效;命令行传入的值优先生效。文件与设置分开存——文件只存二进制本体,元数据只在内存,重启后 `data/` 里的遗留文件会被清掉。

### 示例

```bash
# 自定义端口和存储目录
justswap -port 9000 -download-dir ./uploads

# 保留 24 小时,关闭不清空
justswap -retention 86400 -clear-on-shutdown=false
```

## API

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/meta` | 服务器信息(版本、别名、设置) |
| GET | `/api/files` | 文件列表(按上传时间倒序) |
| POST | `/api/upload?name=` | 上传文件(name URL 编码) |
| GET | `/api/download?name=` | 下载文件(支持 Range) |
| DELETE | `/api/files?name=` | 删除文件 |
| POST | `/api/clear` | 清空全部文件 |
| PATCH | `/api/settings` | 更新保留时长 / 关闭清空开关 |
| GET | `/api/events` | SSE 事件流(文件变更实时推送) |

### 上传示例

```bash
curl -X POST --data-binary @photo.jpg \
  "http://localhost:8787/api/upload?name=photo.jpg"
```

### 下载示例

```bash
# 完整下载
curl -o photo.jpg "http://localhost:8787/api/download?name=photo.jpg"

# 断点续传
curl -H "Range: bytes=0-999" -o partial.jpg \
  "http://localhost:8787/api/download?name=photo.jpg"
```

## 安全说明

**无认证、开放访问。** 局域网内任何设备都可以上传、浏览、下载、删除文件。

- 启动时终端会打印无认证警告
- 网页顶部有可见警告标识
- 路径穿越、绝对路径、Windows 盘符等恶意文件名一律拒绝
- 文件元数据不持久化,重启后无法列出遗留文件

适用于局域网内信任环境(家庭、办公室)。不要暴露到公网。

## 项目结构

```
main.go                      参数解析、启动
internal/app/                进程装配与生命周期
internal/core/               集中状态(内存索引 + 事件总线)
internal/store/              磁盘操作(文件名清洗、流式写入、目录清理)
internal/server/             HTTP 路由与 handler
internal/webui/              内嵌网页 UI(中文)
```

## 校验

本仓库当前没有 `*_test.go`,`go test` 只会输出 "no test files"。用下面三条做格式与静态检查:

```bash
gofmt -l .        # 无输出 = 已格式化
go vet ./...      # 无输出 = 通过
go build -o js.exe .
```

建议补上的集成测试(用 `httptest` 起真路由):上传→列表→下载校验内容一致、Range 206、删除与清空、TTL 过期清理、设置 PATCH 持久化、SSE 推送、路径穿越与 Windows 盘符文件名一律 400。
