<#
restart-web.ps1 —— 重新编译并重启浏览器界面的服务，尽量不断线。

为什么不能直接跑 install.bat 再手工重启：
  install.bat 只做 `go build -o %GOPATH%\bin\agent-dev.exe`。服务还在跑的时候，
  那个 exe 被进程占着，Windows 拒绝写入（实测报错：The process cannot access the file
  because it is being used by another process），go build 当场失败；而且它只装不启，
  装完还得有人去停服务、再起服务。

这个脚本按"先编好，再换，再起"的顺序来，把不能用的时间压到最短：
  1. 编译到 agent-dev.exe.new —— 这一步旧服务照常跑，用户感觉不到
  2. 停旧进程，旧 exe 改名留底（agent-dev.exe.old），.new 顶到正式名字
  3. 后台起新的（隐藏窗口，输出写进日志），然后 TCP 探活
  4. 起不来就把旧 exe 换回去重新拉起，别把服务弄丢

断开的时间就是第 2、3 步之间那几百毫秒。浏览器那边 EventSource 自己会重连，
连上之后前端会重拉一次快照补齐历史，页面上看到的是"已重新连上服务端"。

代价：正在跑的那一轮会被打断（会话文件是每轮结束才落盘）。所以重启前手头
没有在跑的活，或者认了这一轮的损失。

**只能在网页上操作、碰不到这台机器的键盘时**用 -Detach：
  pwsh -File restart-web.ps1 -Detach
它把自己以"完全脱离"的方式再起一份（新进程的父不是当前 shell，也不继承它的
输出管道），然后立刻返回。脱离出去的那份会先等着服务空闲（-WaitIdle：轮询
GET /api/sessions，等所有会话的 status 都不是 running，凭证从配置的 WebAuth 读），
等到了才动手。
所以可以从会话里（agent 自己就是服务的一部分）触发重启而不切断当前这一轮：
这一轮的 done 一落盘，running 变 false，它才开始停进程换 exe。
超时（-IdleTimeout，默认 600 秒）还没空闲就**放弃并写日志**，不会强杀——
手上有长任务时重启本来就该等。

用法：
  pwsh -File restart-web.ps1                 # 编译 + 重启（沿用当前进程的 --web 地址）
  pwsh -File restart-web.ps1 -Addr :8005     # 指定地址（现在没进程在跑时也用它）
  pwsh -File restart-web.ps1 -Status         # 只看现在谁在跑、地址和端口通不通
  pwsh -File restart-web.ps1 -Stop           # 只停，不重启
  pwsh -File restart-web.ps1 -Exe D:\x\agent-dev.exe   # 换安装位置（默认 %GOPATH%\bin）
  pwsh -File restart-web.ps1 -Detach         # 脱离当前 shell 在后台等空闲再重启（web 里用这个）
  pwsh -File restart-web.ps1 -WaitIdle       # 前台跑，但先等空闲
#>
param(
    [string]$Addr = "",
    [string]$Exe = "",
    # 启动目录决定"默认工作区"（会话按工作区分开存）。默认用脚本所在的仓库目录，
    # 原来在别的目录起的服务就用这个参数指回去，不然侧栏会多出一个工作区
    [string]$WorkDir = "",
    [switch]$Stop,
    [switch]$Status,
    [int]$WaitSeconds = 25,
    # -Detach：脱离当前 shell 在后台跑（web 会话里触发重启用这个，见开头说明）
    [switch]$Detach,
    # -WaitIdle：动手前先等服务空闲（没有会话在跑）
    [switch]$WaitIdle,
    # 等空闲最多等多久，超时就放弃（不硬杀正在跑的活）
    [int]$IdleTimeout = 600,
    # -Detach 时第二份自己的输出写到哪儿，默认仓库下的 agent-dev.restart.log
    [string]$Log = ""
)

$ErrorActionPreference = "Stop"
$repo = $PSScriptRoot
if (-not $WorkDir) { $WorkDir = $repo }

# -Log 是给 -Detach 起来的后台实例用的：WMI 创建的进程没有 stdout/stderr，
# 它自己把输出记进日志文件，人才能查"到底走到哪一步了"
if ($Log -and -not $Detach) {
    try {
        Start-Transcript -Path $Log -Append -Force -UseMinimalHeader | Out-Null
        Write-Host "=== $(Get-Date -Format 'yyyy-MM-dd HH:mm:ss') 后台重启开始 ==="
    } catch {
        # 写不了日志不该挡住重启本身
    }
}

# 找现在在跑的浏览器界面进程。终端界面（不带 --web）不归这个脚本管。
# 给了 $onlyAddr 就只要这个地址上的那些：同一台机器另开一个（换个端口试新版）时，
# 不该把两个一起停掉
function Get-WebProcs([string]$onlyAddr) {
    $list = @(Get-CimInstance Win32_Process -Filter "Name='agent-dev.exe'" -ErrorAction SilentlyContinue |
        Where-Object { $_.CommandLine -and $_.CommandLine -match '--?web' })
    if ($onlyAddr) {
        $want = (Get-ProbeTarget $onlyAddr).Port
        $list = @($list | Where-Object { (Get-ProbeTarget (Get-AddrOf $_.CommandLine)).Port -eq $want })
    }
    return $list
}

# 从命令行里取 --web 的地址。认 --web :8005 / --web=:8005 / -web 127.0.0.1:9000 三种写法
function Get-AddrOf([string]$cmdLine) {
    if ($cmdLine -match '--?web=(\S+)') { return $matches[1] }
    if ($cmdLine -match '--?web\s+([^\s]+)') {
        $a = $matches[1]
        if (-not $a.StartsWith('-')) { return $a }
    }
    return "127.0.0.1:8080"
}

# ":8005" 这种是听所有网卡，探活就往 127.0.0.1 上连
function Get-ProbeTarget([string]$addr) {
    $probeHost = "127.0.0.1"
    $port = 0
    if ($addr -match '^([^:]*):(\d+)$') {
        $port = [int]$matches[2]
        $h = $matches[1]
        if ($h -and $h -ne "0.0.0.0" -and $h -ne "::" -and $h -ne "[::]") { $probeHost = $h.Trim('[', ']') }
    }
    return @{ Host = $probeHost; Port = $port; Label = "${probeHost}:${port}" }
}

# 探活只认"端口能连上"：配了登录验证时 HTTP 会回 401，那也是活着的
function Test-Alive($target) {
    if (-not $target.Port) { return $false }
    $client = New-Object Net.Sockets.TcpClient
    try {
        $task = $client.ConnectAsync($target.Host, $target.Port)
        if (-not $task.Wait(800)) { return $false }
        return $client.Connected
    } catch {
        return $false
    } finally {
        $client.Dispose()
    }
}

# Get-WebAuth 从配置文件里挖出登录用的账号密码。配了 WebAuth 的实例，所有接口
# 都要 Basic 认证，脚本自己查"有没有会话在跑"就得带上它。
# 用正则而不是引 yaml 库：只取这两行，不值得为它多一个依赖。
# 注意别把密码打到日志里
function Get-WebAuth([string]$cfgPath) {
    if (-not $cfgPath -or -not (Test-Path -LiteralPath $cfgPath)) { return $null }
    $text = ""
    try { $text = Get-Content -Raw -LiteralPath $cfgPath } catch { return $null }
    $block = [regex]::Match($text, '(?ms)^WebAuth:[ \t]*\r?\n((?:[ \t]+\S.*\r?\n?)+)')
    if (-not $block.Success) { return $null }
    $body = $block.Groups[1].Value
    $u = [regex]::Match($body, '(?m)^[ \t]*[Uu]sername:[ \t]*(.+?)[ \t]*$')
    $p = [regex]::Match($body, '(?m)^[ \t]*[Pp]assword:[ \t]*(.+?)[ \t]*$')
    if (-not $u.Success -or -not $p.Success) { return $null }
    $user = $u.Groups[1].Value.Trim().Trim('"').Trim("'")
    $pass = $p.Groups[1].Value.Trim().Trim('"').Trim("'")
    if (-not $user -or -not $pass) { return $null }
    return @{ User = $user; Pass = $pass }
}

# Get-RunningSessions 问服务"现在有几条会话在跑"。
# 返回 -1 表示问不出来（服务没起 / 认证不对 / 接口变了）——这种时候别拿它当
# "空闲"用，宁可当作不知道
#
# 必须遍历**所有工作区**：GET /api/sessions 不带 ws 只列默认工作区，
# 而人可能正在别的工作区里聊（实测就踩到了——默认工作区一条 running 都没有，
# 当前这一轮在另一个工作区里跑着，只看默认的会把"正忙"误判成"空闲"，
# 然后直接把这一轮切掉）
function Get-RunningSessions($target, $auth) {
    $base = "http://{0}:{1}" -f $target.Host, $target.Port
    $headers = @{}
    if ($auth) {
        $pair = "{0}:{1}" -f $auth.User, $auth.Pass
        $headers["Authorization"] = "Basic " + [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($pair))
    }

    # 工作区列表拿不到就没法遍历，按"不知道"处理。
    # 注意别用 @(...) 包 Invoke-RestMethod：它本来就返回 Object[]，
    # 再包一层会变成"一个元素，那个元素是数组"（实测 count 从 7 变成 1），
    # 于是 $sp.id 拼成一串带空格的假 id，每个都 404
    try {
        $spaces = Invoke-RestMethod -Uri "$base/api/workspaces" -Headers $headers `
            -TimeoutSec 5 -UseBasicParsing -ErrorAction Stop
    } catch {
        return -1
    }
    if ($null -eq $spaces) { return -1 }

    $total = 0
    $asked = 0
    foreach ($sp in $spaces) {
        $id = $sp.id
        $uri = if ($id) { "$base/api/sessions?ws=$id" } else { "$base/api/sessions" }
        try {
            $list = Invoke-RestMethod -Uri $uri -Headers $headers -TimeoutSec 5 -UseBasicParsing -ErrorAction Stop
        } catch {
            continue
        }
        $asked++
        $total += @($list | Where-Object { $_.status -eq "running" }).Count
    }
    if ($asked -eq 0) { return -1 }
    return $total
}

# Wait-Idle 等到没有会话在跑再返回 $true；超时返回 $false。
# "在跑" 的定义和服务端一致（Status() == running），也就是说这一轮的 done
# 已经落盘、界面也收到了。所以在会话里触发重启不用担心把自己这轮切断
function Wait-Idle($target, $auth, [int]$seconds) {
    $deadline = (Get-Date).AddSeconds($seconds)
    $unknown = 0
    while ((Get-Date) -lt $deadline) {
        $n = Get-RunningSessions $target $auth
        if ($n -eq 0) { return $true }
        if ($n -lt 0) {
            $unknown++
            # 问不出来不能死等：连续问不到就当服务已经没了（这种情况下重启正是要干的事）
            if ($unknown -ge 5) { Write-Host "查不到会话状态，当作没有在跑的活继续" -ForegroundColor Yellow; return $true }
        } else {
            $unknown = 0
            Write-Host ("  还有 {0} 条会话在跑，等它们跑完再动手…" -f $n) -ForegroundColor DarkGray
        }
        Start-Sleep -Seconds 2
    }
    return $false
}

# Start-Detached 把"自己"以脱离的方式再起一份，然后立刻返回。
# 为什么要脱离：agent（也就是在网页上跟你对话的那个东西）本身就跑在服务进程里，
# 它去执行这个脚本时，脚本是它的孙进程。服务一停，顺着进程树找下来的东西都会
# 被一起收掉（工具层中断时就是 taskkill /F /T 整棵树），而且服务一死，
# 子进程继承的那两个输出管道就断了，脚本连"换 exe 成功"都打不出来。
# Start-Process 起的新进程不继承输出管道（重定向到文件），父进程退出也不影响它
function Start-Detached {
    # 变量别叫 $exe：参数里有 $Exe（安装位置），PowerShell 变量名不区分大小写，
    # 在这里赋 $exe 就等于把 $Exe 改成了 pwsh 自己的路径，
    # 脱离出去的那份会拿着 pwsh.exe 当 agent-dev 去换、去起（实测报"不认识的参数 -NoLogo"）
    $psExe = (Get-Process -Id $PID).Path
    $self = $PSCommandPath
    if (-not $self) { throw "取不到脚本自身的路径（$PSCommandPath），-Detach 需要它" }
    $logPath = if ($Log) { $Log } else { Join-Path $repo "agent-dev.restart.log" }

    # 参数拼成一个命令行字符串，而不是传数组：数组会被 PowerShell 再拼一遍，
    # 我手动加的引号会跟着进到参数里去（实测 -File 收到的路径带引号，pwsh 找不到脚本）。
    # 单个字符串是原样的命令行，引号由 pwsh 自己解析，这是唯一稳的写法
    $cmd = "-NoProfile -ExecutionPolicy Bypass -File `"$self`""
    if ($Addr) { $cmd += " -Addr `"$Addr`"" }
    if ($Exe) { $cmd += " -Exe `"$Exe`"" }
    if ($WorkDir) { $cmd += " -WorkDir `"$WorkDir`"" }
    $cmd += " -WaitIdle -IdleTimeout $IdleTimeout -WaitSeconds $WaitSeconds -Log `"$logPath`""

    # 用 WMI 创建，不用 Start-Process：Start-Process 起的子进程会攥着当前 shell 的
    # stdout 管道不放，调用方（比如 agent 自己的工具、或者任何捕获输出的终端）
    # 就一直等到超时——实测卡满 3 分钟，重启本身早完成了却看不到结果。
    # WMI 创建出来的进程父是 WmiPrvSE，完全脱离这棵进程树：
    #   1. 调用方立刻拿回控制权（实测 44ms）
    #   2. 服务被停、整棵树被 taskkill /T 都波及不到它
    # 代价是它没有 stdout，所以子实例自己用 Start-Transcript 写日志（见下面 -Log 分支）
    $cmdLine = "`"$psExe`" $cmd"
    $created = Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{
        CommandLine      = $cmdLine
        CurrentDirectory = $repo
    }
    if ($created.ReturnValue -ne 0) {
        throw "WMI 起后台进程失败（ReturnValue=$($created.ReturnValue)），这次没重启，服务照原样跑着"
    }
    $newPid = $created.ProcessId

    # 等它把日志文件建出来再报成功：WMI 只保证"进程创建成功"，
    # 子进程是不是立刻挂了得看日志有没有开始写
    $deadline = (Get-Date).AddSeconds(5)
    while ((Get-Date) -lt $deadline) {
        if (Test-Path -LiteralPath $logPath) { break }
        Start-Sleep -Milliseconds 150
    }
    $alive = Get-Process -Id $newPid -ErrorAction SilentlyContinue
    if (-not $alive -and -not (Test-Path -LiteralPath $logPath)) {
        throw "后台实例 PID $newPid 起来就退了，也没有日志出来。这次没重启，服务照原样跑着"
    }

    Write-Host "已在后台脱离运行（PID $newPid）：等空闲（最多 $IdleTimeout 秒）后重启" -ForegroundColor Cyan
    Write-Host "  日志：$logPath" -ForegroundColor DarkGray
    Write-Host "  这一步不受本进程死活影响：你继续看页面就行，断开会自己重连" -ForegroundColor DarkGray
    return $newPid
}

# 停进程并等它真的退干净：没退干净的话 exe 还占着，改名会失败。
# 判"退干净了"按 PID 一个个查，不用 Get-WebProcs 的计数——CIM 查询偶发返回空，
# 那会让脚本以为已经停了，接着换名、起新进程，而旧进程其实还占着端口
function Stop-WebProcs($procs) {
    $pids = @($procs | ForEach-Object { $_.ProcessId })
    foreach ($id in $pids) {
        try {
            Stop-Process -Id $id -Force -ErrorAction Stop
            Write-Host "  已停 PID $id" -ForegroundColor DarkGray
        } catch {
            Write-Warning "停 PID $id 失败：$($_.Exception.Message)"
        }
    }
    $deadline = (Get-Date).AddSeconds(10)
    while ((Get-Date) -lt $deadline) {
        $alive = @($pids | Where-Object { Get-Process -Id $_ -ErrorAction SilentlyContinue })
        if ($alive.Count -eq 0) { return $true }
        Start-Sleep -Milliseconds 150
    }
    return $false
}

function Build-NewExe([string]$exePath) {
    $new = "$exePath.new"
    if (Test-Path $new) { Remove-Item $new -Force }
    Push-Location $repo
    try {
        & go build -o $new ./src/cli
        if ($LASTEXITCODE -ne 0) { throw "go build 失败（exit $LASTEXITCODE）" }
    } finally {
        Pop-Location
    }
    if (-not (Test-Path $new)) { throw "编译完了却没看到 $new" }
    return $new
}

function Start-WebExe([string]$exePath, [string]$webAddr) {
    if (-not (Test-Path $exePath)) { throw "找不到可执行文件：$exePath" }
    $outLog = Join-Path $repo "agent-dev.out.log"
    $errLog = Join-Path $repo "agent-dev.err.log"
    $argv = @()
    if ($webAddr) { $argv = @("--web", $webAddr) } else { $argv = @("--web") }
    $p = Start-Process -FilePath $exePath -ArgumentList $argv -WorkingDirectory $WorkDir `
        -WindowStyle Hidden -PassThru -RedirectStandardOutput $outLog -RedirectStandardError $errLog
    return $p
}

# Assert-ExeTarget 是换文件前的最后一道闸：确认 $Exe 真的该被换成 agent-dev。
#
# 这条闸是血换来的。2026-11-06 我在 Start-Detached 里写了 `$exe = (Get-Process -Id $PID).Path`，
# 而参数里已经有 `[string]$Exe`——PowerShell 变量名不区分大小写，两者是同一个变量。
# 于是脚本认为"agent-dev 该装到 C:\Program Files\PowerShell\7\pwsh.exe"，
# 停掉服务、把用户的 pwsh.exe 改名成 .old、把 agent-dev 顶上去替换掉。
# 结果整台机器的 pwsh 全废（Bash 工具每次调用都报"不认识的参数 -NoLogo"）。
#
# 所以不管路径从哪来（参数、默认值、还是某个变量被谁改过），动手前一律验：
#   1. 文件名必须是 agent-dev.exe
#   2. 不许落在系统目录里（Program Files / Windows / System32）
#   3. 服务正在跑的时候，还必须和它自己用的路径一致
# 任一条不过就停，什么都不动
function Assert-ExeTarget([string]$exePath, $procs) {
    if (-not $exePath) { throw "要装到哪儿的路径是空的，不敢动" }

    $leaf = Split-Path -Leaf $exePath
    if ($leaf -ne "agent-dev.exe") {
        throw "拒绝操作：目标文件名是「$leaf」，不是 agent-dev.exe。完整路径 $exePath"
    }

    $full = ""
    try { $full = [IO.Path]::GetFullPath($exePath) } catch { $full = $exePath }
    $blocked = @(
        [Environment]::GetFolderPath("ProgramFiles"),
        [Environment]::GetFolderPath("ProgramFilesX86"),
        $env:SystemRoot,
        (Join-Path $env:SystemRoot "System32")
    ) | Where-Object { $_ }
    foreach ($dir in $blocked) {
        if ($full.StartsWith($dir, [StringComparison]::OrdinalIgnoreCase)) {
            throw "拒绝操作：$full 落在系统目录 $dir 里，这里不是放 agent-dev 的地方"
        }
    }

    # 服务在跑就以它为准：它自己用的那个 exe 才是该被替换的那个
    $running = @($procs | ForEach-Object {
            if ($_.CommandLine -match '^\s*"([^"]+)"') { $matches[1] }
            elseif ($_.CommandLine -match '^\s*(\S+\.exe)') { $matches[1] }
        } | Where-Object { $_ } | Select-Object -Unique)
    if ($running.Count -eq 1) {
        $livePath = $running[0]
        if (-not [string]::Equals([IO.Path]::GetFullPath($livePath), $full,
                [StringComparison]::OrdinalIgnoreCase)) {
            throw ("拒绝操作：正在跑的服务用的是 $livePath，" +
                "而这次要换的是 $full。两者对不上，说明路径算错了，先查清楚再动手")
        }
    }
    return $full
}

# 起完等端口通，最多等 $WaitSeconds 秒
function Wait-Alive($target, [int]$seconds) {
    $deadline = (Get-Date).AddSeconds($seconds)
    while ((Get-Date) -lt $deadline) {
        if (Test-Alive $target) { return $true }
        Start-Sleep -Milliseconds 200
    }
    return $false
}

# ---------- 开始 ----------

# -Detach 只做一件事：把带 -WaitIdle 的自己脱离出去，然后立刻返回。
# 放在最前面，连 go 都不检查——第一次判断该走哪条路的时候别因为环境问题先崩
if ($Detach) {
    $null = Start-Detached
    exit 0
}

if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    throw "找不到 go 命令：PATH 里没有 Go 工具链，先把它加进去"
}

$allProcs = Get-WebProcs

if (-not $Exe) {
    # 变量名一律用 $Exe 本身，不要写成小写的局部变量：PowerShell 变量名不区分大小写，
    # 另起一个 $exe 就是在改 $Exe（这正是 2026-11-06 把 pwsh.exe 换掉的原因）
    $gopath = (& go env GOPATH).Trim()
    $Exe = Join-Path $gopath "bin\agent-dev.exe"
}

# 校验目标路径，验不过就停在这里——这时候还没编译、没停进程、没碰任何文件
try {
    $Exe = Assert-ExeTarget $Exe $allProcs
} catch {
    Write-Host "✗ $($_.Exception.Message)" -ForegroundColor Red
    Write-Host "  什么都没动：没编译、没停服务、没换文件。" -ForegroundColor DarkGray
    exit 4
}

# 地址优先级：命令行指定 > 当前进程自己在用的 > 默认
if (-not $Addr) {
    if ($allProcs.Count -gt 0) { $Addr = Get-AddrOf $allProcs[0].CommandLine }
    else { $Addr = "127.0.0.1:8080" }
}
$target = Get-ProbeTarget $Addr

if ($WaitIdle -and -not $Status -and -not $Stop) {
    # 先等服务空闲再动手。会话里触发重启时，这一轮还没结束，此刻停进程会把
    # 刚才那段对话整段丢掉（会话是每轮结束才落盘）
    Write-Host "先等服务空闲（最多 $IdleTimeout 秒）…" -ForegroundColor Cyan
    $auth = Get-WebAuth (Join-Path $HOME ".agent-dev\config.yaml")
    if ($auth) { Write-Host "  用配置里的 WebAuth 账号查会话状态（用户名 $($auth.User)）" -ForegroundColor DarkGray }
    if (Wait-Idle $target $auth $IdleTimeout) {
        Write-Host "服务空闲了，开始重启" -ForegroundColor Green
    } else {
        Write-Host ("等满 {0} 秒还有会话在跑，放弃这次重启：手上有长任务时本来就不该重启。" +
            "要强制就用 -WaitIdle:$false（-Detach 时除外）") -f $IdleTimeout -ForegroundColor Red
        Write-Host "（放弃前什么都没动：没编译、没停进程、没换 exe）" -ForegroundColor DarkGray
        exit 3
    }
}

# 只动这个地址上的那个服务。同一台机器另开的（换了端口的）只提一句，不碰
$procs = @(Get-WebProcs $Addr)
$stopIds = @($procs | ForEach-Object { $_.ProcessId })
$others = @($allProcs | Where-Object { $stopIds -notcontains $_.ProcessId })
# 备份 exe 用的时间戳。带上它就不会和上次留下的备份撞名，
# 也就永远不需要"先删旧备份"这个可能失败的步骤
$stamp = Get-Date -Format "yyyyMMdd-HHmmss"

if ($Status) {
    Write-Host "可执行文件：$Exe" -ForegroundColor Cyan
    Write-Host "重启会用的启动目录（= 默认工作区）：$WorkDir" -ForegroundColor Cyan
    if (-not (Test-Path $Exe)) { Write-Host "  这个文件不存在" -ForegroundColor Yellow }
    if ($procs.Count -eq 0) {
        Write-Host "没有浏览器界面的服务在跑（地址 $Addr）" -ForegroundColor Yellow
    } else {
        foreach ($p in $procs) {
            Write-Host "PID $($p.ProcessId)  地址 $(Get-AddrOf $p.CommandLine)  启动于 $($p.CreationDate)" -ForegroundColor Green
        }
    }
    foreach ($p in $others) {
        Write-Host "另有 PID $($p.ProcessId)（$(Get-AddrOf $p.CommandLine)）不在这个地址上，不动它" -ForegroundColor DarkGray
    }
    if (Test-Alive $target) {
        Write-Host "端口 $($target.Label) 通" -ForegroundColor Green
    } else {
        Write-Host "端口 $($target.Label) 不通" -ForegroundColor Yellow
    }
    exit 0
}

if ($procs.Count -eq 0 -and -not $Stop) {
    Write-Host "没有服务在跑，直接起一个新的（地址 $Addr）…" -ForegroundColor Cyan
} elseif ($procs.Count -gt 1) {
    Write-Host "地址 $Addr 上有 $($procs.Count) 个服务在跑，都会停掉" -ForegroundColor Yellow
}

if ($Stop) {
    if ($procs.Count -eq 0) { Write-Host "本来就没在跑" -ForegroundColor Yellow; exit 0 }
    Write-Host "停服务…" -ForegroundColor Cyan
    if (-not (Stop-WebProcs $procs)) { throw "进程没退干净，去任务管理器看一下" }
    Write-Host "已停" -ForegroundColor Green
    exit 0
}

# 1. 先编译到 .new：这一步旧服务照常跑
$exeDir = Split-Path -Parent $Exe
if ($exeDir -and -not (Test-Path $exeDir)) { New-Item -ItemType Directory -Path $exeDir -Force | Out-Null }
Write-Host "编译…" -ForegroundColor Cyan
$newExe = Build-NewExe $Exe
Write-Host "  编译好了：$newExe" -ForegroundColor DarkGray

# 2. 先换文件，再停进程。
# 这个顺序是被坑出来的：
#   - 备份名带时间戳，不覆盖 "$Exe.old"。实测 "$Exe.old" 那种固定名有删不掉的时候
#     （ACL 明明给了 FullControl，Remove-Item 仍报 Access denied，但重命名可以），
#     一删不掉整个重启就失败，而那时旧进程已经停了——服务就这么没了。
#     唯一名永不撞车，不需要先删任何东西。
#   - 换文件放在停进程之前：Windows 允许重命名正在运行的 exe（映像已加载，不影响运行），
#     实测重命名后服务照跑。所以这一步失败时旧服务毫发无损，什么都没坏。
$bak = "$Exe.$stamp.bak"
if (Test-Path $Exe) {
    try {
        Rename-Item -LiteralPath $Exe -NewName (Split-Path -Leaf $bak) -ErrorAction Stop
    } catch {
        Remove-Item -LiteralPath $newExe -Force -ErrorAction SilentlyContinue
        Write-Host "✗ 换不掉 $Exe：$($_.Exception.Message)" -ForegroundColor Red
        Write-Host "  什么都没动：旧服务还在跑，还是旧版本。" -ForegroundColor DarkGray
        exit 5
    }
}
try {
    Move-Item -LiteralPath $newExe -Destination $Exe -ErrorAction Stop
} catch {
    # 顶不上去就把原来的名字还回来，别留下"exe 不见了"的局面
    if (Test-Path $bak) { Rename-Item -LiteralPath $bak -NewName (Split-Path -Leaf $Exe) -ErrorAction SilentlyContinue }
    Write-Host "✗ 新 exe 顶不上去：$($_.Exception.Message)" -ForegroundColor Red
    Write-Host "  已把原来的 exe 名字还原，旧服务还在跑。" -ForegroundColor DarkGray
    exit 5
}
Write-Host "  文件已换好（备份 $($bak)），现在停旧进程" -ForegroundColor DarkGray

# 3. 停旧进程
$wasAlive = $procs.Count -gt 0
if ($wasAlive) {
    Write-Host "停旧进程…" -ForegroundColor Cyan
    if (-not (Stop-WebProcs $procs)) { throw "进程没退干净，新旧都起不来；先手工处理再说" }
}

# 4. 起新的，等端口通
$clock = [Diagnostics.Stopwatch]::StartNew()
Write-Host "起新服务（$Addr）…" -ForegroundColor Cyan
$webProc = $null
try {
    $webProc = Start-WebExe $Exe $Addr
} catch {
    Write-Host "启动失败：$($_.Exception.Message)" -ForegroundColor Red
}

$ok = Wait-Alive $target $WaitSeconds

# 探通还不够：得确认响应的就是这个新进程。旧进程要是有残留（CIM 没看到、或者刚退到一半），
# 端口照样通，脚本会喜报"好了"，而跑的还是旧代码——那比报错更难查
if ($ok -and $webProc) {
    if ($webProc.HasExited) {
        Write-Host "新进程起来后退了（exit $($webProc.ExitCode)）" -ForegroundColor Red
        $ok = $false
    } else {
        $listener = @(Get-NetTCPConnection -LocalPort $target.Port -State Listen -ErrorAction SilentlyContinue |
            Where-Object { $_.OwningProcess -eq $webProc.Id })
        if ($listener.Count -eq 0) {
            Write-Host "端口 $($target.Label) 通了，但不是新进程（PID $($webProc.Id)）在听，先别当成功" -ForegroundColor Red
            $ok = $false
        }
    }
}
$clock.Stop()

if ($ok) {
    Write-Host ("好了：$Addr 已通，断开约 {0:N1} 秒" -f $clock.Elapsed.TotalSeconds) -ForegroundColor Green
    Write-Host "日志：$(Join-Path $repo 'agent-dev.out.log') / agent-dev.err.log" -ForegroundColor DarkGray
    # 新版本确认能跑起来，这次的备份就可以清了。
    # 用 -ErrorAction SilentlyContinue：这一步失败完全无所谓（顶多留个备份文件），
    # 而且实测这类文件有时真删不掉，绝不能因为这个把已经成功的重启判成失败
    if (Test-Path $bak) { Remove-Item -LiteralPath $bak -Force -ErrorAction SilentlyContinue }
    # 顺手把更早的备份也收拾一下，只留最新的一个，免得 go\bin 越攒越多
    Get-ChildItem -LiteralPath (Split-Path -Parent $Exe) -Filter "agent-dev.exe.*.bak" -ErrorAction SilentlyContinue |
        Sort-Object LastWriteTime -Descending | Select-Object -Skip 1 |
        ForEach-Object { Remove-Item -LiteralPath $_.FullName -Force -ErrorAction SilentlyContinue }
    exit 0
}

# 5. 起不来：把旧的换回去，别让服务就这么没了
# 这里必须用 Write-Host 而不是 Write-Error：$ErrorActionPreference = "Stop" 下
# Write-Error 会当场终止脚本，下面这段回滚一行都跑不到——那正是"重启脚本不会自动
# 恢复、还得手动启动"的原因（实测：pwsh 7 里 Write-Error 之后的语句全部不执行）
Write-Host "新版本 $WaitSeconds 秒内没起来，回滚…" -ForegroundColor Red
Get-WebProcs $Addr | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
Start-Sleep -Milliseconds 500
if (Test-Path $bak) {
    # 同样不靠"删掉新版本"：先把它重命名挪开（重命名比删除可靠），再把备份改名回来
    $broken = "$Exe.$stamp.broken"
    try {
        if (Test-Path $Exe) { Rename-Item -LiteralPath $Exe -NewName (Split-Path -Leaf $broken) -ErrorAction Stop }
        Rename-Item -LiteralPath $bak -NewName (Split-Path -Leaf $Exe) -ErrorAction Stop
    } catch {
        Write-Host "✗ 换回旧 exe 失败：$($_.Exception.Message)" -ForegroundColor Red
        Write-Host "  手工处理：把 $bak 改名成 $Exe" -ForegroundColor Yellow
    }
    $backProc = $null
    try {
        $backProc = Start-WebExe $Exe $Addr
    } catch {
        Write-Warning "旧版本也没起来：$($_.Exception.Message)"
    }
    # 和上面一样，光"端口通"不算数：得是这次起来的 agent-dev 自己在听
    $backOK = Wait-Alive $target 10
    if ($backOK -and $backProc) {
        $listener = @(Get-NetTCPConnection -LocalPort $target.Port -State Listen -ErrorAction SilentlyContinue |
            Where-Object { $_.OwningProcess -eq $backProc.Id })
        if ($listener.Count -eq 0) { $backOK = $false }
    }
    if ($backOK) {
        Write-Host "已回滚到旧版本，服务在跑（看 agent-dev.err.log 查新版本为什么没起来）" -ForegroundColor Yellow
        if (Test-Path $broken) { Remove-Item -LiteralPath $broken -Force -ErrorAction SilentlyContinue }
    } else {
        Write-Host "回滚也没成功，去 agent-dev.err.log 看原因" -ForegroundColor Red
    }
}
exit 1
