# Meshify

[English](README.md) | [简体中文](README.zh-CN.md)

Meshify 是一个 Go 命令行工具，用一个 `meshify.yaml` 把一台 Debian 系服务器部署成单机 Headscale 控制面：Nginx 负责 HTTPS 入口，Headscale 负责私有 Tailnet，内置 DERP/STUN 处理连通性兜底，客户端使用官方 Tailscale 加入。

## 快速开始

在目标服务器下载已发布的 Release 二进制。把 `vX.Y.Z` 替换成对应 release tag：

```bash
VERSION=vX.Y.Z
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64) ASSET=meshify_linux_amd64 ;;
  aarch64|arm64) ASSET=meshify_linux_arm64 ;;
  *) echo "unsupported architecture: $ARCH" >&2; exit 1 ;;
esac

curl -LO "https://github.com/simp-lee/meshify/releases/download/${VERSION}/${ASSET}"
curl -LO "https://github.com/simp-lee/meshify/releases/download/${VERSION}/checksums.txt"
sha256sum -c --ignore-missing checksums.txt
chmod +x "${ASSET}"
sudo install -m 0755 "${ASSET}" /usr/local/bin/meshify
meshify --help
```

如果使用源码 checkout 而不是 Release 二进制，运行 `make build` 后把 `./meshify` 安装到 `/usr/local/bin/meshify`。

然后执行默认流程 `init -> deploy -> verify`，再用只读状态命令检查结果：

```bash
meshify init --config meshify.yaml
sudo meshify deploy --config meshify.yaml
meshify verify --config meshify.yaml
meshify status --config meshify.yaml
```

部署前，把公网域名（例如 `hs.example.com`）解析到服务器，并放行 `80/tcp`、`443/tcp`、`3478/udp`。服务端验证通过后，使用 deploy 输出的 preauth key 或重新生成新 key，再按下方客户端章节操作。

## 支持范围

| 范围 | 基线 |
| --- | --- |
| 服务器系统 | Debian、Ubuntu，或具备 apt/dpkg/systemd 的 Debian 系发行版 |
| 控制面 | Headscale v0.28.0 只监听本机，由 Nginx 对外代理 |
| TLS 自动化 | HTTP-01 或 DNS-01，使用 Meshify 管理的固定版本 lego v4.35.2 |
| 中继 | Headscale 内置 DERP/STUN，监听 `3478/udp`；不接入官方 DERP 列表 |
| 客户端 | Windows, macOS, Debian/Ubuntu Linux |
| 客户端基线 | Tailscale client >= v1.74.0 |

Meshify 刻意保持范围小：不做多机高可用、Kubernetes、Terraform、Ansible、Web UI、OIDC/SSO、SQLite 自动备份恢复、官方 DERP 兜底，也默认不开放远程 gRPC/API-key 管理。

## 服务器端指南

### 部署前准备

- 服务器权限：Debian、Ubuntu，或 `/etc/os-release` 通过 `ID` / `ID_LIKE` 报告 `debian` 或 `ubuntu` 的 Debian 系发行版上的 root 或免密 sudo。
- 主机能力：部署前必须能使用 `apt-get`、`dpkg` 和已启动的 systemd runtime。
- DNS：把 `hs.example.com` 这样的公网 Headscale 域名解析到服务器。
- 防火墙：本机防火墙和云安全组都要放行 `80/tcp`、`443/tcp`、`3478/udp`。
- 包来源：服务器要能下载 Headscale `.deb` 和固定版本 lego archive；不能直连时准备镜像或离线包。
- 客户端：至少准备两台不同网络环境下的客户端做最终验证。
- 中国大陆公网部署：提前确认 ICP/接入规则、云入口、包下载可达性、代理设置和 HTTP-01 是否可行；必要时使用 DNS-01、镜像、离线包或代理。

### 最小配置

公开示例在 [`deploy/config/meshify.yaml.example`](deploy/config/meshify.yaml.example)。多数首次部署只需要编辑 `default`：

```yaml
default:
  server_url: "https://hs.example.com"
  base_domain: "tailnet.example.com"
  certificate_email: "ops@example.com"
  acme_challenge: "http-01"
```

`server_url` 是客户端 `tailscale up --login-server` 使用的 HTTPS 地址，必须是 DNS 域名，通常使用 443 端口。`base_domain` 是私有 MagicDNS 后缀，不能等于 Headscale 主机名，也不能是它的父域名。

只有需要 DNS-01、Headscale 镜像/离线包、Headscale metrics 端口、离线 lego、代理、架构覆盖或公网 IP 覆盖时，才使用高级引导：

```bash
meshify init --advanced --config meshify.yaml
```

### ACME

公网 80 能访问服务器时，使用 HTTP-01：

```yaml
default:
  acme_challenge: "http-01"
```

只有公网 80 不可靠或组织策略要求 DNS 验证时才使用 DNS-01。DNS-01 使用 lego provider code `cloudflare`、`route53`、`digitalocean`、`gcloud`，其中 `google` 可作为 `gcloud` 别名。

不要把 DNS API 值写进 `meshify.yaml`。Cloudflare 和 DigitalOcean 需要 root-only 的 `advanced.dns01.env_file`；Route53 和 gcloud 可以在部署和 systemd 续期使用同一主机身份时走 lego 的环境凭据链。原始 DNS token 或 key 放在单独的 root-only 文件中，并通过 lego `_FILE` 变量引用。

### 部署

在目标服务器运行：

```bash
sudo meshify deploy --config meshify.yaml
```

`deploy` 会检查配置、系统家族、主机能力、权限、DNS、端口、包来源、ACME 前置条件和服务冲突；随后安装依赖、安装 lego 和 Headscale、写入运行时文件、申请证书、启用服务、创建初始 Headscale 用户和 preauth key，并执行静态验证。

如果中途失败，按输出里的失败步骤修复后重复同一条 `deploy` 命令即可。Meshify 会把 checkpoint 写在配置文件旁边的 `.meshify/` 目录。

### 验证和状态

```bash
meshify verify --config meshify.yaml
meshify status --config meshify.yaml
```

`verify` 会重新检查 Headscale、ACL、Nginx、TLS hook、证书计划、onboarding 准备状态，以及 Tailscale 客户端版本基线。`meshify status` 是只读命令，用来查看配置状态、已完成 checkpoint、警告和上次可恢复失败。

预期结果：

- Headscale 控制面、metrics、gRPC 都只监听本机。
- Nginx 从 `/var/lib/meshify/acme-challenges` 处理 HTTP-01，使用 `fullchain.pem` 终止 TLS，并转发控制面和 DERP WebSocket 所需的 HTTP/1.1 upgrade。
- Nginx 使用 `/etc/meshify/tls/<server>/fullchain.pem` 和 `/etc/meshify/tls/<server>/privkey.pem`。
- Headscale 在 `3478/udp` 暴露 STUN，使用内置 DERP，`derp.urls` 保持为空。
- 两台不同网络里的客户端能加入、解析 MagicDNS、互相 `tailscale ping`，并在 `tailscale netcheck` 中看到直连或 DERP 兜底路径。

### 运行拓扑

```text
Internet
  |
  | 80/tcp, 443/tcp
  v
+------------------------------------------------+
| Debian-family apt/dpkg/systemd host            |
|                                                |
| Nginx                                          |
|   - HTTP-01 webroot                            |
|   - TLS termination with fullchain.pem         |
|   - reverse proxy to 127.0.0.1:8080            |
|                                                |
| Headscale                                      |
|   - control plane on loopback                  |
|   - metrics on configurable loopback port      |
|   - gRPC on loopback                           |
|   - local admin over unix socket               |
|   - embedded DERP and STUN on 3478/udp         |
|                                                |
| meshify-managed lego                           |
|   - certificate issue/renew                    |
|   - install hook reloads Nginx after validate  |
+------------------------------------------------+
  |
  v
Clients prefer direct WireGuard paths and fall back to DERP over 443.
```

### 安全边界

- Go CLI 是唯一面向用户的服务器端入口。
- 公网 HTTP/HTTPS 只到 Nginx；Headscale 控制面不绑定公网网卡。
- 明确的 HTTP/HTTPS `default_server` catch-all 会拒绝不匹配 Host/SNI 的流量，而不是转发给 Headscale。
- Headscale 管理默认只走本机 unix socket；不要开放远程 gRPC 或 API-key 管理，除非你明确需要并自行配置。
- DNS-01 服务商敏感值不能写入 `meshify.yaml`、渲染模板、deploy/status 输出或 systemd unit。

### 服务端排障

先看失败的命令。`meshify deploy`、`meshify verify`、`meshify status` 会在可恢复时输出失败步骤、影响、修复建议和重试命令。

配置检查：

- `server_url` 必须是 HTTPS DNS 域名，只能省略端口或使用 443。
- `base_domain` 不能等于 `server_url` 主机名，也不能是它的父域。
- `certificate_email` 必须是普通邮箱地址。
- `acme_challenge` 只能是 `http-01` 或 `dns-01`。

前置检查阻塞：

- DNS 必须先把公网 Headscale 域名解析到目标服务器。
- 本机和云安全组都要允许 `80/tcp`、`443/tcp`、`3478/udp`。
- 已有 Nginx 可按 `server_name` 共存，但 Meshify 会管理 HTTP/HTTPS `default_server` catch-all。部署前迁移冲突的默认站点。

软件包和 lego：

- direct 模式会下载固定的 Headscale v0.28.0 `.deb` 并校验 SHA-256。
- mirror 模式需要可访问 URL 和明确的 SHA-256。
- offline 模式需要本地 `.deb` 路径和明确的 SHA-256。
- lego 离线模式要求 `advanced.lego_source.file_path` 指向匹配 `advanced.platform.arch` 的固定版本 archive。

运行时失败：

- Headscale 应监听 `127.0.0.1:8080`。
- Metrics 应监听 `127.0.0.1:<advanced.headscale.metrics_port>`，默认 `19090`。
- gRPC 应监听 `127.0.0.1:50443`。
- 启用内置 DERP 时，`3478/udp` 需要留给 STUN。
- Headscale 启动失败时查看 `systemctl status headscale.service --no-pager --full` 和 `/etc/headscale/config.yaml`。
- Nginx 失败时运行 `nginx -t` 并检查 `/etc/nginx/sites-available/headscale.conf`。
- 如果登录或 DERP 在 Nginx 后异常，确认 HTTP/1.1 Upgrade 和 Connection 头仍被转发。

排障时请收集完整的 `meshify deploy`、`meshify verify` 或 `meshify status` 输出、`default` 里改过的值、Headscale 来源模式，以及失败影响的是 deploy、证书签发、Nginx、Headscale、MagicDNS、直连路径选择，还是 DERP 兜底。

## 客户端指南

请在 `meshify deploy` 和 `meshify verify` 通过后使用本章节。

### 运维交接

请把以下信息交给每个客户端用户：

- `server_url`，例如 `https://hs.example.com`。
- 一个新的、一次性的 preauth key。deploy 输出的 key 足够接入第一台设备；每增加一台客户端，都重新生成一个 key。
- `base_domain` 对应的 MagicDNS 后缀，例如 `tailnet.example.com`。
- 本章节里对应平台的操作步骤。

Headscale 管理保持在本机。默认配置使用 `/var/run/headscale/headscale.sock`，首日部署不要开放远程 gRPC 或 API-key 管理。

### 生成新的 preauth key

`meshify deploy` 会在 Headscale 运行后创建初始 `meshify` 用户和一次性 preauth key。Headscale 的 preauth key 默认不可复用：下面这条命令创建的 key 只能接入一台客户端，并在 24 小时后过期。要接入更多客户端，就再次执行命令，为每台客户端分别发一个不同的 key。

```bash
sudo headscale --config /etc/headscale/config.yaml users list
# Only if the meshify user is missing from users list:
sudo headscale --config /etc/headscale/config.yaml users create meshify
sudo headscale --config /etc/headscale/config.yaml users list
sudo headscale --config /etc/headscale/config.yaml preauthkeys create --user <ID> --expiration 24h
```

`<ID>` 使用 `users list` 中 `meshify` 用户对应的数字 ID。一次性接入建议使用短有效期。只有明确想让多个客户端共用同一个 key 时，才额外加 `--reusable`：

```bash
sudo headscale --config /etc/headscale/config.yaml preauthkeys create --user <ID> --expiration 24h --reusable
```

可复用 key 更适合受控自动化或短时间维护窗口，不建议作为终端用户设备的默认交付方式。

### 统一验证目标

每个受支持客户端都应做到：

- 安装 Tailscale client >= v1.74.0
- 使用提供的 `server_url` 和 preauth key 加入
- 通过 `--accept-dns=true` 或平台 UI 等价流程接受托管 DNS
- 在 `tailscale status` 中显示在线
- 能用 `tailscale ping` 访问另一台节点
- 能用 `tailscale netcheck` 查看路径信息

至少用两台不同网络里的客户端验证，例如家庭宽带 + 办公网，或家庭宽带 + 手机热点。UDP 穿透可用时应优先直连 WireGuard；UDP 直连被阻断时，只要互通正常，走 TCP/443 的 DERP 兜底是可接受的。

`tailscale debug derp-map` 是可选排查命令，应只看到自建 DERP region。内置 DERP 不提供 `/generate_204`；不要只因为 captive portal 探测异常就判定部署失败，先验证真实登录、`tailscale status`、`tailscale ping`、MagicDNS 和 `tailscale netcheck`。

### Windows

从 <https://tailscale.com/download/windows> 安装 Tailscale client >= v1.74.0，也可以使用 Microsoft Store。首次配置自定义 login server 时，请打开管理员 PowerShell。

```powershell
& "$env:ProgramFiles\Tailscale\tailscale.exe" version
& "$env:ProgramFiles\Tailscale\tailscale.exe" up --login-server https://hs.example.com --auth-key "<preauth-key>" --accept-dns=true --hostname=laptop
& "$env:ProgramFiles\Tailscale\tailscale.exe" status
& "$env:ProgramFiles\Tailscale\tailscale.exe" ping peer-name.tailnet.example.com
& "$env:ProgramFiles\Tailscale\tailscale.exe" netcheck
```

日常操作：

```powershell
& "$env:ProgramFiles\Tailscale\tailscale.exe" down
& "$env:ProgramFiles\Tailscale\tailscale.exe" up --login-server https://hs.example.com --accept-dns=true
```

### macOS

从 <https://tailscale.com/download/mac> 安装 Tailscale client >= v1.74.0。默认推荐 standalone package，也支持 Mac App Store 版本。

图形界面路径：按住 Option 点击菜单栏 Tailscale 图标，打开 Debug 菜单，选择自定义 login server，填入 `server_url`，然后完成 key 或登录流程。

如果安装渠道提供 `tailscale` 命令，也可以用 CLI：

```bash
tailscale up --login-server https://hs.example.com --auth-key <preauth-key> --accept-dns=true --hostname=laptop
tailscale status
tailscale ping peer-name.tailnet.example.com
tailscale netcheck
```

日常操作：

```bash
tailscale down
tailscale up --login-server https://hs.example.com --accept-dns=true
```

### Debian/Ubuntu Linux

使用 Tailscale 官方 Linux 包源安装 Tailscale client >= v1.74.0：

```bash
curl -fsSL https://tailscale.com/install.sh | sh
tailscale version
systemctl status tailscaled --no-pager --full
sudo tailscale up --login-server https://hs.example.com --auth-key <preauth-key> --accept-dns=true --hostname=laptop
tailscale status
tailscale ping peer-name.tailnet.example.com
tailscale netcheck
```

如果环境不允许 `curl | sh`，请按 <https://tailscale.com/download/linux> 的 Debian 或 Ubuntu 手动步骤安装，也可以使用运维提供的包文件。

日常操作：

```bash
sudo tailscale down
sudo tailscale up --login-server https://hs.example.com --accept-dns=true
```

### 客户端排障

- 重新核对 `server_url`、preauth key 是否过期，以及 key 是否已被消费。
- 确认客户端版本满足 Tailscale client >= v1.74.0。
- MagicDNS 不解析时，确认客户端接受了托管 DNS，且 `base_domain` 正确。
- 如果路径一直是 DERP，但 UDP 直连被网络阻断且互通正常，这可以接受；真正失败是节点完全不通。
- 如果找不到 `tailscale`，按上面的平台章节重新安装。
- Windows 常见问题是防火墙或终端安全软件阻断虚拟网卡。
- macOS 常见问题是未批准 VPN 提示，或 captive portal/受限 Wi-Fi 影响登录。
- Linux 常见问题是 `tailscaled` 没有运行，或 `/dev/net/tun` 不存在。
- 内置 DERP 不提供 `/generate_204`；请验证真实登录、`tailscale status`、`tailscale ping`、MagicDNS 和 `tailscale netcheck`。

排障时请收集平台、客户端版本、`server_url`、是否接受托管 DNS，以及失败影响的是登录、MagicDNS、直连路径选择，还是 DERP 兜底。
