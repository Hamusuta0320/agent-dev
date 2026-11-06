# 第三方样式库

这里放的是原样下载的前端库文件，**不要手改**，升级就整份替换。

| 文件 | 来源 | 版本 | 许可 |
| --- | --- | --- | --- |
| `bootstrap.min.css` | https://cdn.jsdelivr.net/npm/bootstrap@5.3.3/dist/css/bootstrap.min.css | 5.3.3 | MIT |
| `bootstrap.bundle.min.js` | https://cdn.jsdelivr.net/npm/bootstrap@5.3.3/dist/js/bootstrap.bundle.min.js | 5.3.3 | MIT |

放本地而不是引 CDN：这个工具的页面是 `go:embed` 打进二进制的，
要能在内网、断网的机器上照常打开，页面里不能出现任何外链。

升级步骤：

```powershell
$dir = "src/webui/static/vendor"
Invoke-WebRequest "https://cdn.jsdelivr.net/npm/bootstrap@5.3.3/dist/css/bootstrap.min.css" -OutFile "$dir/bootstrap.min.css"
Invoke-WebRequest "https://cdn.jsdelivr.net/npm/bootstrap@5.3.3/dist/js/bootstrap.bundle.min.js" -OutFile "$dir/bootstrap.bundle.min.js"
```

换版本后要顺手改上面表格里的版本号，再 `go build` 让 embed 生效。
