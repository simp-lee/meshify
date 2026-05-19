# Meshify

[English](README.md) | [简体中文](README.zh-CN.md)

Meshify 是一个用 Go 写的服务器部署工具，不是 VPN 客户端。它适合想自建私有 Tailscale/Headscale 网络、但不想手工配置 Headscale、Nginx、HTTPS 证书和 systemd 的个人或小团队：准备一台 Debian/Ubuntu 云服务器、一个域名和 sudo 权限，Meshify 就会按 `meshify.yaml` 完成部署；也可以用 `meshify app` 把同机或 tailnet 内的 HTTP/WebSocket 服务挂到公网 HTTPS。如果你只是想在云主机上跑自己的 Go Web 服务，也可以只用 app 流程，不部署私有 Tailscale/Headscale 网络。

## 先判断是否适合

| 你想做什么 | Meshify 是否适合 |
| --- | --- |
| 用官方 Tailscale 客户端加入自己的私有网络 | 适合，Meshify 部署的是 Headscale 控制面 |
| 在一台云服务器上自动配置 Headscale、Nginx、证书续期和基础验证 | 适合，这是本仓库的核心目标 |
| 把同机 Go Web 服务或 tailnet 内 HTTP/WebSocket 服务发布到 HTTPS | 适合，使用 `meshify app` |
| 只发布云主机本机的 Go Web 服务，不搭私有 Tailscale/Headscale 网络 | 适合，使用 `meshify app` 的 `listen` 模式 |
| 搭多机高可用、Kubernetes、Terraform、Ansible、Web UI、OIDC/SSO | 不适合，这些不在当前范围内 |
| 找 Tailscale 客户端、通用反向代理框架或 Go SDK | 不适合，本仓库主要是 CLI、配置示例和运行时模板 |

## 快速开始

部署前先做三件事：

- 准备一台 Debian/Ubuntu 或 Debian 系服务器，并有 root 或免密 sudo。
- 把公网域名，例如 `hs.example.com`，解析到这台服务器。
- 放行 `80/tcp`、`443/tcp`、`3478/udp`，本机防火墙和云安全组都要放行。

如果你只想部署本机 Go Web 服务，不创建私有网络，只需要 app 域名和 `80/tcp`、`443/tcp`；`3478/udp` 只用于主 Headscale 部署。

在目标服务器下载 Release 二进制。把 `vX.Y.Z` 替换成对应 release tag：

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

然后按默认流程执行 `init -> deploy -> verify`，最后用只读状态命令检查结果。部署前务必检查 `meshify.yaml`；如果里面仍是示例值，至少把 `default.server_url`、`default.base_domain` 和 `default.certificate_email` 改成你的真实值。

```bash
meshify init --config meshify.yaml
# 检查 meshify.yaml；如仍是示例值，先编辑 default 段
sudo meshify deploy --config meshify.yaml
meshify verify --config meshify.yaml
meshify status --config meshify.yaml
```

`deploy` 通过后，CLI 会输出初始 preauth key。先用它接入第一台客户端，后续每台客户端建议重新生成一个新的 key。

上面这套默认流程用于部署私有 Tailscale/Headscale 网络。如果只是发布本机 Go 服务，可以跳过 `meshify init` 和 `meshify deploy` 主流程，直接看下面的“同机部署其它 Go 服务”章节。

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

字段含义：

| 字段 | 作用 |
| --- | --- |
| `server_url` | 客户端 `tailscale up --login-server` 使用的 HTTPS 地址，必须是 DNS 域名，通常使用 443 |
| `base_domain` | 私有 MagicDNS 后缀，不能等于 Headscale 主机名，也不能是它的父域名 |
| `certificate_email` | ACME 注册邮箱 |
| `acme_challenge` | `http-01` 或 `dns-01` |

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

主部署拓扑如下。公网只应该看到 Nginx 的 HTTP/HTTPS 和 Headscale STUN 端口；Headscale 控制面、metrics 和 gRPC 都留在 loopback。

```text
Internet, ACME CA, and Tailscale clients
  |                         \
  | 443/tcp control + DERP   \ 3478/udp STUN
  | 80/tcp HTTP-01            \
  v                            v
+------------------------------------------------+
| Debian-family apt/dpkg/systemd host            |
|                                                |
| Nginx                                          |
|   - HTTP-01 webroot                            |
|   - TLS termination with fullchain.pem         |
|   - reverse proxy to 127.0.0.1:8080            |
|   - HTTP/1.1 upgrade for DERP WebSocket        |
|                                                |
| Headscale                                      |
|   - control plane on 127.0.0.1:8080            |
|   - metrics on 127.0.0.1:<metrics_port>        |
|   - gRPC on 127.0.0.1:50443                    |
|   - local admin over unix socket               |
|   - embedded DERP over HTTPS proxy path        |
|   - STUN on 3478/udp                           |
|                                                |
| meshify-managed lego                           |
|   - certificate issue/renew                    |
|   - install hook reloads Nginx after validate  |
+------------------------------------------------+
```

客户端之间优先走直连 WireGuard；直连失败时，经 Nginx 代理的内置 DERP 通过 443 兜底。

如果同机部署 Go 服务，拓扑会多出一个 app 站点。app 端口仍然只监听本机，不直接暴露公网。

```text
Internet
  |
  | app domain: 80/tcp, 443/tcp
  v
+------------------------------------------------+
| Nginx app site                                 |
|   - app certificate and Host/SNI allowlist     |
|   - optional static alias locations            |
|   - proxy / to 127.0.0.1:18001                |
|                                                |
| example-app.service                            |
|   - runs as app.name system user               |
|   - ExecStart from service.exec_start          |
|   - listens on loopback only                   |
+------------------------------------------------+
```

只部署本机 Go 服务时，可以把上面的 app 站点当成完整拓扑：公网进 Nginx，Nginx 转发到本机 Go 服务，不需要 Headscale 或 Tailscale client。

如果发布的是 tailnet 内其它节点上的 HTTP/WebSocket 服务，公网入口仍在这台云服务器，但 upstream 走本机 Tailscale client 转发到固定 `100.64.x.y:port`。

```text
Internet
  |
  | app domain: 80/tcp, 443/tcp
  v
+------------------------------------------------+
| Nginx app site                                 |
|   - app certificate and Host/SNI allowlist     |
|   - proxy / to fixed tailnet upstream          |
|                                                |
| Tailscale client on this host                  |
|   - logged in to expected login server         |
+------------------------------------------------+
  |
  | tailnet HTTP/WebSocket
  v
100.64.x.y:port on another tailnet node
```

### 同机部署其它 Go 服务

如果要把自己的 Go Web 服务也放到这台云服务器，或把 tailnet 内某个 HTTP/WebSocket 服务通过这台服务器发布到公网，使用独立 app 配置和 `meshify app` 流程。

如果你不想搭私有 Tailscale/Headscale 网络，只想让公网通过 HTTPS 访问这台云主机上的 Go 服务，也用这里的 `listen` 模式。这个模式不会要求你先部署 Headscale；只要保持 `upstream: ""`，并且不要把 `tailscale.enabled_for_listen` 改成 `true`，Meshify 就只处理本机 app、Nginx、证书和 systemd。

新手先按这条最短路径走：生成配置、编辑配置、静态校验、部署。

```bash
meshify app init --config meshify-apps/abc.yaml
# 编辑 meshify-apps/abc.yaml
meshify app verify --config meshify-apps/abc.yaml
sudo meshify app deploy --config meshify-apps/abc.yaml
```

app 流程不提供 `--example`：`meshify app init` 本身就会写出可编辑示例配置；`--example` 只属于主 `meshify init` 命令。示例配置源在 [`deploy/config/meshify-app.yaml.example`](deploy/config/meshify-app.yaml.example)，`meshify app init` 会写出同样结构的可编辑文件。

推荐一个 app 一个配置文件，并统一放在 `meshify-apps/` 目录：

```text
meshify.yaml
meshify-apps/abc.yaml
meshify-apps/admin.yaml
meshify-apps/tailapp.yaml
```

只部署 app 时可以没有 `meshify.yaml`；上面的目录只是同时管理主私有网络和多个 app 时的常见放法。

配置文件名不是部署身份；真正的部署身份来自 `app.name`。重命名配置文件不会重命名 systemd unit、Nginx 站点或证书目录。

#### 先选模式

| 模式 | 什么时候用 | 你要准备什么 |
| --- | --- | --- |
| `listen` | Go 服务就跑在这台云服务器上 | 把业务二进制安装到 `service.exec_start` 第一个 token 指向的绝对路径，并让服务监听 loopback |
| `upstream` | 后端服务在 tailnet 内其它节点上，例如 `100.64.10.20:18001` | 确认云服务器上的 Tailscale client 能访问这个固定 HTTP/WebSocket upstream |

`listen` 表示本机 app 模式：Go 服务运行在同一台云服务器上，只监听 loopback，例如 `127.0.0.1:18001`。Meshify 会生成 app systemd service、Nginx 站点、证书、hook 和续期 timer；它只验证业务二进制存在且可执行，不复制你的业务二进制。

`upstream` 表示 tailnet upstream 模式：公网 Nginx 反代到 tailnet 内其它节点的固定 HTTP/WebSocket 地址，例如 `100.64.10.20:18001`。该模式不生成本机 app service。`listen` 和 `upstream` 必须二选一；`upstream` 模式自动需要 Tailscale client。`upstream` 只适合 HTTP/WebSocket 服务，不用于 PostgreSQL、Redis、MySQL 等数据库端口公网发布。

#### 最小 app 配置

`listen` 模式示例：

```yaml
api_version: meshify/app/v1alpha1

app:
  name: "example-app"
  domains:
    - "abc.com"
    - "www.abc.com"
  certificate_email: "ops@example.com"
  acme_challenge: "http-01"
  listen: "127.0.0.1:18001"
  upstream: ""

service:
  exec_start: "/opt/example-app/example-app --listen 127.0.0.1:18001"
  working_directory: "/opt/example-app"
  env_file: ""

tailscale:
  enabled_for_listen: false
```

把 `abc.com` 换成你的真实 app 域名，把 `/opt/example-app/example-app` 换成你已经上传到服务器的 Go 二进制路径。你的 Go 程序监听 `127.0.0.1:18001`，公网用户访问的是 Nginx 提供的 `https://abc.com`，不要把 `18001` 直接开放到公网。

`upstream` 模式示例：

```yaml
app:
  name: "tailapp"
  domains:
    - "tailapp.example.com"
  certificate_email: "ops@example.com"
  acme_challenge: "http-01"
  listen: ""
  upstream: "100.64.10.20:18001"

service:
  exec_start: ""
  working_directory: ""
  env_file: ""
```

多个域名写在同一个 `app.domains` 列表中。所有域名会写入同一个 Nginx `server_name`、同一张证书的 SAN，以及 Host/SNI allowlist。首版不会自动做 `abc.com` 和 `www.abc.com` 之间的 canonical redirect；它们默认服务同一个 app。

如果 app 需要读取 `web.env` 这类 systemd 环境文件，在 `service.env_file` 中填写绝对路径；deploy 会校验它是 root-owned、root-only 文件，并渲染为 `EnvironmentFile=`。`service.env_file` 只在 `listen` 模式使用。

#### 进阶 app 配置

首次部署可以先不改 `nginx` 段。需要上传大文件、长连接、SSE、WebSocket 或静态文件时，再看这些字段：

| 配置 | 默认值或作用 |
| --- | --- |
| `nginx.client_max_body_size` | 默认 `20m` |
| `nginx.http2` | 默认开启，渲染现代 `http2 on;` 指令 |
| `nginx.access_log` / `nginx.error_log` | 可指向 app 独立日志文件，`access_log` 也可设为 `off` |
| `proxy.read_timeout` | app 反代读取超时，默认 `600s` |
| `proxy.send_timeout` | app 反代发送超时，默认 `600s` |
| `proxy.connect_timeout` | 可选连接超时 |
| `proxy.buffering` / `proxy.request_buffering` | 适合流式响应、SSE 或上传转发场景 |
| `nginx.static_locations` | 从业务发布目录暴露 `/static/`、`/sitemap.xml`、`/sitemaps/` 等静态文件 |

`nginx.static_locations` 会渲染在 app 反代 location 之前，支持可选 `expires`、`Cache-Control`、`try_files $uri =404`、`gzip_static on` 和 `access_log off`。Meshify 不复制静态文件内容；静态文件应由业务发布流程和业务二进制一起放到对应 release 路径。

`nginx.http2` 为 true 或任一静态 location 设置 `gzip_static: true` 时，app deploy 会在写入 runtime 文件前检查 `nginx -V`；`http2 on;` 要求 Nginx 至少为 `1.25.1` 且包含 `http_v2` 模块。

#### Tailscale 逻辑

- `upstream` 模式自动需要 Tailscale client。
- `listen` 模式只有在本机 app 也需要主动访问 tailnet 时才设置 `tailscale.enabled_for_listen: true`。
- 如果本机 app 不需要访问 tailnet，保持 `tailscale.enabled_for_listen` 为 false，并忽略 `tailscale` 段其余字段。
- `tailscale.login_server` 为空时，Meshify 从 `tailscale.meshify_config` 指向的主 `meshify.yaml` 读取 `default.server_url`。
- `tailscale.meshify_config` 为空时，默认使用当前目录的 `meshify.yaml`。
- `tailscale.hostname` 为空时不传 `--hostname`，让 Tailscale 使用系统 hostname。
- `tailscale.auth_key_file` 只保存 root-only auth key 文件路径，不保存 key 内容。

deploy 会先检查 Tailscale client 是否已安装、已运行、已登录期望 login server。已经满足时会跳过安装、跳过创建 preauth key、跳过重新登录。未登录时，本机 Meshify 管理的 Headscale 会自动创建短期 preauth key；外部 Headscale 使用 `tailscale.auth_key_file` 或预先登录好的 client。

自动登录时，Meshify 会运行带 `--login-server` 和 `--auth-key` 的 `tailscale up`，并固定附加这些策略参数：

```bash
--accept-dns=false --accept-routes=false --shields-up
```

如果当前机器已经登录到无法证明匹配的 login server，deploy 会显式失败，不会自动 `logout`、清状态或重入网。

#### app verify 和状态

`meshify app verify` 是 app 流程的静态配置和模板检查，状态通过时 CLI 输出 `static-passed`。它会校验 app schema、模板渲染、Nginx Host/SNI guard、证书路径、systemd 计划、Tailscale 需求推导和敏感值泄露；它不读取宿主机上的已部署文件、systemd 状态、证书 SAN、Nginx runtime 或 Tailscale 在线状态。`meshify app deploy` 也会先执行同类静态检查。

主流程的 `meshify status` 读取主部署 checkpoint、activation history 和上次可恢复失败；app 首版没有独立 checkpoint store，因此不提供 `meshify app status`。

release binary 的 app runtime 模板唯一来源是 `deploy/templates/app/`，并由 `meshify app deploy` 自动渲染和安装。

#### app 部署前检查

- `app.domains` 都解析到当前云服务器。
- 对 app 站点，公网只通过 Nginx 开放 `80/tcp` 和 `443/tcp`，不开放 `18001` 这类 app 端口；如果同机也运行主 Headscale 部署，`3478/udp` 仍然需要留给 STUN。
- `listen` 模式业务二进制已经安装，且 `service.exec_start` 的第一个 token 是可执行文件绝对路径。
- 如设置 `service.env_file`，它必须指向 root-owned、root-only 文件。
- `nginx.static_locations` 的 alias 指向已经随 app release 发布的文件或目录。
- `upstream` 模式后端是固定 `100.64.x.y:port`，并且从云服务器可以访问。
- DNS-01 的 `dns01.env_file` 和 Tailscale 的 `tailscale.auth_key_file` 都是 root-only 文件。

已部署宿主机状态仍用 `curl`、`nginx -t`、证书检查和 `systemctl` 验证。只有 `upstream` 模式或设置了 `tailscale.enabled_for_listen: true` 的 `listen` app 才需要看 `tailscale status`。`upstream` 模式没有本机 app service，可跳过 `<app-name>.service` 检查，并改为从云服务器确认固定 tailnet upstream 可访问。

### 安全边界

- Go CLI 是唯一面向用户的服务器端入口。
- 公网 HTTP/HTTPS 只到 Nginx；Headscale 控制面不绑定公网网卡。
- 明确的 HTTP/HTTPS `default_server` catch-all 会拒绝不匹配 Host/SNI 的流量，而不是转发给 Headscale。
- Headscale 管理默认只走本机 unix socket；不要开放远程 gRPC 或 API-key 管理，除非你明确需要并自行配置。
- DNS-01 服务商敏感值不能写入 `meshify.yaml`、渲染模板、deploy/status 输出或 systemd unit。
- app 端口应只监听 loopback 或固定 tailnet upstream，不要把业务私有端口直接暴露公网。

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

app 运行时失败：

- `listen` 模式先确认业务二进制存在、可执行，并且真的监听 `app.listen`。
- `listen` 模式查看 `systemctl status <app-name>.service --no-pager --full`。
- `upstream` 模式先在云服务器上测试固定 upstream 是否可达。
- app 域名证书或反代异常时运行 `nginx -t`，并检查 `/etc/nginx/sites-available/<app-name>.conf`。
- 静态文件 404 时确认 `nginx.static_locations` 的 `alias` 文件已经由业务发布流程放好。

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

图形界面路径适用于已经登录过至少一个其它 tailnet 的 App Store 或 standalone 客户端：点击菜单栏 Tailscale 图标，打开 Settings，进入 Accounts，选择左下角下拉箭头，填入 `server_url` 并添加账号。全新客户端请直接使用 CLI。

如果安装渠道提供 `tailscale` 命令，也可以用 CLI：

```bash
tailscale up --login-server https://hs.example.com --auth-key <preauth-key> --accept-dns=true --hostname=laptop
tailscale set --hostname=<name>
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
tailscale set --hostname=<name>
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
