# 远程访问 Drove

Drove 的 HTTP listener 始终绑定 loopback。远程浏览器通过 Tailscale Serve 或 SSH
端口转发访问这个 listener。不要把 `api_bind` 改成非 loopback 地址。

Tailscale Serve 适合手机和需要 Web Push 的浏览器。SSH 端口转发适合临时从另一台
电脑访问。两种方式都继续使用 Drove 的一次性登录码和 HttpOnly cookie。

## 使用 Tailscale Serve

### 前置条件

- Drove 主机和访问设备已加入同一个 tailnet。
- Drove 主机已启用 MagicDNS 和 HTTPS。
- Drove 主机已安装 `tailscale`。
- 本机有 `curl` 和 `jq`，用于签发一次性登录链接。

### 配置允许的 HTTPS Origin

先确认 Tailscale 为 Drove 主机分配的完整 DNS 名称：

```bash
tailscale status
```

假设名称是 `drove-host.example.ts.net`。把对应 HTTPS Origin 加入
`~/.drove/config.json`。保留已有的本地开发 Origin。

```json
{
  "api_bind": "127.0.0.1:7373",
  "console_origins": [
    "http://localhost:5173",
    "http://127.0.0.1:5173",
    "https://drove-host.example.ts.net"
  ]
}
```

重启 `droved`，让新的 Host 和 Origin 白名单生效。daemon 仍只监听
`127.0.0.1:7373`。

### 启动 HTTPS 反向代理

在 Drove 主机上运行：

```bash
tailscale serve --bg http://127.0.0.1:7373
tailscale serve status
```

Serve 输出的地址必须与 `console_origins` 中的 HTTPS Origin 完全一致。Drove 不读取
Tailscale 身份请求头。每个浏览器仍需使用 Drove 的一次性登录链接。

### 签发远程登录链接

在 Drove 主机上运行下面的命令。`DATA_DIR` 必须与 Drove 配置一致。一次性登录码在
两分钟后过期，而且只能使用一次。

```bash
DATA_DIR="${DROVE_DATA_DIR:-$HOME/.drove}"
ORIGIN="https://drove-host.example.ts.net"
CODE="$(
  {
    printf 'header = "Host: drove.local"\n'
    printf 'header = "Authorization: Bearer %s"\n' \
      "$(tr -d '\r\n' < "$DATA_DIR/control.token")"
  } |
    curl --config - \
      --fail-with-body \
      --silent \
      --show-error \
      --unix-socket "$DATA_DIR/run/droved.sock" \
      --request POST \
      http://drove.local/api/v1/auth/login-code |
    jq -er '.code'
)"
printf '%s/login#%s\n' "${ORIGIN%/}" "$CODE"
unset CODE
```

在链接过期前，用远程设备打开输出的 URL。URL fragment 不会经过 Tailscale
Serve。Drove 页面把一次性登录码兑换成有效期 12 小时的 HttpOnly cookie，并从地址
栏删除 fragment。

不要通过不可信渠道发送这个 URL。任何先使用未过期链接的人都能获得一个 Drove
浏览器会话。

### 安装 PWA 并启用通知

1. 在远程设备上打开已登录的控制台。
2. 在 iOS Safari 中选择“分享”，再选择“添加到主屏幕”。Android Chrome 和桌面
   浏览器可以使用浏览器的安装入口。
3. 从已安装的 PWA 打开 Drove。
4. 打开页眉中的通知面板。
5. 选择“启用此设备”，再选择“发送测试”。

iOS 只有从主屏幕启动的 PWA 能申请 Web Push 权限。所有平台都需要允许通知。
浏览器拒绝权限后，需要从站点设置中重新授权。

停止共享 Drove：

```bash
tailscale serve --https=443 off
```

这个命令删除默认 HTTPS Serve 映射。使用 `tailscale serve reset` 前，先通过
`tailscale serve status` 检查其他映射。

## 使用 SSH 端口转发

从客户端电脑建立转发，并保持命令运行：

```bash
ssh -N -L 7373:127.0.0.1:7373 user@drove-host
```

在 Drove 主机的另一个 shell 中签发一次性链接。使用上一节的命令，但把
`ORIGIN` 改为：

```bash
ORIGIN="http://127.0.0.1:7373"
```

在客户端浏览器中打开生成的链接。浏览器连接本机 `127.0.0.1:7373`，SSH 再把流量
转发到 Drove 主机。loopback HTTP 属于浏览器安全上下文，因此桌面浏览器可以注册
service worker 和 Web Push。

如果客户端的 7373 端口已被占用，可以选择其他本地端口。此时还要把精确 Origin
加入 Drove 主机的 `console_origins`，然后重启 daemon。例如，本地端口 17373 对应
`http://127.0.0.1:17373`。

SSH 连接断开后，浏览器无法打开 Drove 或处理通知中的详情链接。Web Push
subscription 仍保存在 `notify.db`，直到用户在通知面板中停用设备或推送服务使订阅
失效。

## 安全边界

- 不要使用 Tailscale Funnel 或公网端口转发。Drove 没有公网部署边界。
- 不要把 `control.token`、VAPID 私钥或 ntfy token 复制到远程设备。
- `console_origins` 必须包含完整 scheme、host 和可选端口。不要添加通配符。
- Tailscale Serve 提供 HTTPS，但 Drove 仍校验 Host、Origin 和自己的 cookie。
- service worker 不缓存控制台 HTML、登录响应、API、WebSocket 或终端数据。
- 通知只含 Agent 元数据和同源详情链接。远程控制台仍能读取终端和发送输入，因此只
  应对可信设备签发登录链接。
- [#28](https://github.com/Duang777/drove/issues/28) 才会增加通知中的批准、拒绝和
  回复动作。当前通知点击只打开对应 Agent。

Tailscale Serve 的命令格式见
[Tailscale Serve command](https://tailscale.com/kb/1242/tailscale-serve)。
