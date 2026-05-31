# Meshify

[English](README.md) | [简体中文](README.zh-CN.md)

Meshify 是一个用 Go 写的服务器部署工具，不是 VPN 客户端。它适合想自建私有 Tailscale/Headscale 网络、但不想手工配置 Headscale、Nginx、HTTPS 证书和 systemd 的个人或小团队：准备一台 Debian/Ubuntu 云服务器、一个域名和 root 或 sudo 权限，Meshify 就会按 `meshify.yaml` 完成部署；也可以用 `meshify app` 把同机或 tailnet 内的 HTTP/WebSocket 服务挂到公网 HTTPS，并可选启用 GoAccess 实时访问日志看板。如果你只是想在云主机上跑自己的 Go Web 服务，也可以只用 app 流程，不部署私有 Tailscale/Headscale 网络。

## 先判断是否适合

| 你想做什么 | Meshify 是否适合 |
| --- | --- |
| 用官方 Tailscale 客户端加入自己的私有网络 | 适合，Meshify 部署的是 Headscale 控制面 |
| 在一台云服务器上自动配置 Headscale、Nginx、证书续期和基础验证 | 适合，这是本仓库的核心目标 |
| 把同机 Go Web 服务或 tailnet 内 HTTP/WebSocket 服务发布到 HTTPS，并可选启用访问日志看板 | 适合，使用 `meshify app`，按需启用 `nginx.goaccess` |
| 只发布云主机本机的 Go Web 服务，不搭私有 Tailscale/Headscale 网络 | 适合，使用 `meshify app` 的 `listen` 模式 |
| 搭多机高可用、Kubernetes、Terraform、Ansible、Web UI、OIDC/SSO | 不适合，这些不在当前范围内 |
| 找 Tailscale 客户端、通用反向代理框架或 Go SDK | 不适合，本仓库主要是 CLI、配置示例和运行时模板 |

## 快速开始

部署前先做三件事：

- 准备一台 Debian/Ubuntu 或 Debian 系服务器，并能用 root 或 sudo 执行部署。
- 把公网域名，例如 `hs.example.com`，解析到这台服务器。
- 放行 `80/tcp`、`443/tcp`、`3478/udp`，本机防火墙和云安全组都要放行。

如果你只想部署本机 Go Web 服务，不创建私有网络，只需要 app 域名和 `80/tcp`、`443/tcp`；`3478/udp` 只用于主 Headscale 部署。

在目标服务器安装 Release 二进制。先打开 [Releases](https://github.com/simp-lee/meshify/releases) 复制最新稳定 release tag，再把下面的 `vX.Y.Z` 替换成这个 tag；不要原样复制占位符。

```bash
VERSION=vX.Y.Z
curl -fsSL https://raw.githubusercontent.com/simp-lee/meshify/main/scripts/install.sh | sh -s -- "${VERSION}"
meshify --help
```

安装脚本会自动识别 `x86_64` 和 `arm64`/`aarch64`，下载 `VERSION` 对应的 GitHub Release asset，用 `checksums.txt` 校验后，把 `meshify` 安装到 `/usr/local/bin/meshify`。

如果使用源码 checkout 而不是 Release 二进制，运行 `make build` 后把 `./meshify` 安装到 `/usr/local/bin/meshify`。

然后按默认流程执行 `init -> verify -> deploy -> verify -> status`。部署前务必检查 `meshify.yaml`；如果里面仍是示例值，至少把 `default.server_url`、`default.base_domain` 和 `default.certificate_email` 改成你的真实值。

```bash
meshify init --config meshify.yaml
# 检查 meshify.yaml；如仍是示例值，先编辑 default 段
meshify verify --config meshify.yaml
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
| TLS 自动化 | HTTP-01 或 DNS-01，使用 Meshify 管理的固定版本 lego v5.1.0 |
| 中继 | Headscale 内置 DERP/STUN，监听 `3478/udp`；不接入官方 DERP 列表 |
| 客户端 | Windows, macOS, Debian/Ubuntu Linux |
| 客户端基线 | Tailscale client >= v1.74.0 |

Meshify 刻意保持范围小：不做多机高可用、Kubernetes、Terraform、Ansible、Web UI、OIDC/SSO、SQLite 自动备份恢复、官方 DERP 兜底，也默认不开放远程 gRPC/API-key 管理。

## 服务器端指南

### 部署前准备

- 服务器权限：Debian、Ubuntu，或 `/etc/os-release` 通过 `ID` / `ID_LIKE` 报告 `debian` 或 `ubuntu` 的 Debian 系发行版上的 root 或可用 sudo；非 root 直接运行时需通过 `sudo -n true`。
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

只有需要 DNS-01、Headscale 镜像/离线包、Headscale metrics 端口、离线 lego、包来源探测超时覆盖、代理、架构覆盖或公网 IP 覆盖时，才使用高级引导：

```bash
meshify init --advanced --config meshify.yaml
```

如果 GitHub release 下载可达但很慢，可以调大包来源探测超时：

```yaml
advanced:
  package_probe:
    reachability_timeout: "30s"
    artifact_timeout: "5m"
```

### ACME

公网 80 能访问服务器时，使用 HTTP-01：

```yaml
default:
  acme_challenge: "http-01"
```

只有公网 80 不可靠或组织策略要求 DNS 验证时才使用 DNS-01。DNS-01 使用 lego provider code `cloudflare`、`route53`、`digitalocean`、`gcloud`、`tencentcloud`，其中 `google` 可作为 `gcloud` 别名。

不要把 DNS API 值写进 `meshify.yaml`，也不要把原始 token/key 直接写进 `env_file`。Cloudflare、DigitalOcean 和腾讯云需要 root-only 的 `advanced.dns01.env_file`，并用各自支持的 `_FILE` 变量引用 root-only 密钥文件；Route53 和 gcloud 可走主机凭据链，或只在 `env_file` 中放它们支持的 provider 设置或凭据文件路径。

腾讯云 DNS / DNSPod 示例：先在腾讯云或 DNSPod 里创建能管理 DNSPod 解析记录的 API 密钥，然后把 SecretId 和 SecretKey 放进 root-only 文件，再由 lego env 文件引用这些文件：

```bash
sudo install -d -m 0700 /etc/meshify/dns01
printf '%s' '<腾讯云 SecretId>' | sudo tee /etc/meshify/dns01/tencentcloud-secret-id >/dev/null
printf '%s' '<腾讯云 SecretKey>' | sudo tee /etc/meshify/dns01/tencentcloud-secret-key >/dev/null
sudo chmod 0600 /etc/meshify/dns01/tencentcloud-secret-id /etc/meshify/dns01/tencentcloud-secret-key

sudo tee /etc/meshify/dns01/tencentcloud.env >/dev/null <<'EOF'
TENCENTCLOUD_SECRET_ID_FILE=/etc/meshify/dns01/tencentcloud-secret-id
TENCENTCLOUD_SECRET_KEY_FILE=/etc/meshify/dns01/tencentcloud-secret-key
TENCENTCLOUD_PROPAGATION_TIMEOUT=900
TENCENTCLOUD_POLLING_INTERVAL=10
TENCENTCLOUD_TTL=600
TENCENTCLOUD_HTTP_TIMEOUT=60
LEGO_DNS_RESOLVERS=119.29.29.29:53
LEGO_DNS_TIMEOUT=30
LEGO_DNS_PROPAGATION_DISABLE_ANS=true
EOF
sudo chmod 0600 /etc/meshify/dns01/tencentcloud.env
```

其中 `LEGO_DNS_*` 会把 lego 的 DNS zone 判断固定到 DNSPod 公共解析器，保留递归解析器 TXT 轮询，并跳过在腾讯云 EdgeOne / DNSPod 托管接入下容易失败的权威 nameserver 传播检查。配合 `TENCENTCLOUD_POLLING_INTERVAL=10`，lego 会每 10 秒检查一次，TXT 记录对指定递归解析器可见后就继续申请证书。

然后在 `meshify.yaml` 中启用 DNS-01：

```yaml
default:
  acme_challenge: "dns-01"

advanced:
  dns01:
    provider: "tencentcloud"
    env_file: "/etc/meshify/dns01/tencentcloud.env"
```

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

`verify` 是静态配置和 runtime 模板检查。它会重新检查渲染出的 Headscale、ACL、Nginx、TLS hook、证书计划、onboarding 准备状态，以及 Tailscale 客户端版本基线；它不读取宿主机上的 systemd 状态、证书文件、Nginx runtime、Headscale 进程状态或客户端在线状态。`meshify status` 是只读命令，用来查看配置状态、已完成 checkpoint、警告和上次可恢复失败。

部署后的宿主机运行态仍要用系统命令确认：

```bash
sudo systemctl status headscale.service nginx.service --no-pager --full
sudo nginx -t
curl -I https://hs.example.com
```

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
|   - proxy / to 127.0.0.1:18001                 |
|                                                |
| Optional GoAccess log dashboard                |
|   - reads canonical Nginx access log           |
|   - live updates over loopback WebSocket       |
|   - Nginx serves report.html and proxies /ws   |
|   - logrotate for meshify-managed access log   |
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

只部署同机 `listen` app 时可以没有 `meshify.yaml`；上面的目录只是同时管理主私有网络和多个 app 时的常见放法。

`upstream` app 必须使用 Tailscale client；`listen` app 只有主动访问 tailnet 时才设置 `tailscale.enabled_for_listen: true`。`tailscale.login_server` 为空时，Meshify 会从 `tailscale.meshify_config` 读取 `default.server_url`；`tailscale.meshify_config` 为空时默认是命令当前工作目录下的 `./meshify.yaml`，不是 `--config` 旁边的文件。主配置不在当前目录时请显式设置 `tailscale.meshify_config`；没有主配置时请显式设置 `tailscale.login_server`，并提供 root-only 的 `tailscale.auth_key_file` 或提前把这台云服务器登录到对应 tailnet。

deploy 会检查 Tailscale client 是否已安装、运行并登录到期望 login server；已满足时会跳过安装和重登。自动登录固定附加 `--accept-dns=false --accept-routes=false --shields-up`。如果当前机器已经登录到无法证明匹配的 login server，deploy 会失败，不会自动 `logout`、清状态或重入网。

配置文件名不是部署身份；真正的部署身份来自 `app.name`。重命名配置文件不会重命名 systemd unit、Nginx 站点或证书目录。

#### 先选模式

| 模式 | 什么时候用 | 你要准备什么 |
| --- | --- | --- |
| `listen` | Go 服务就跑在这台云服务器上 | 把业务二进制安装到 `service.exec_start` 第一个 token 指向的绝对路径，并让服务监听 loopback |
| `upstream` | 后端服务在 tailnet 内其它节点上，例如 `100.64.10.20:18001` | 确认云服务器上的 Tailscale client 能访问这个固定 HTTP/WebSocket upstream |

`listen` 表示本机 app 模式：Go 服务运行在同一台云服务器上，只监听 loopback，例如 `127.0.0.1:18001`。Meshify 会生成 app systemd service、Nginx 站点、证书、hook 和续期 timer；它只验证业务二进制存在且可执行，不复制你的业务二进制。

`upstream` 表示 tailnet upstream 模式：公网 Nginx 反代到 tailnet 内其它节点的固定 HTTP/WebSocket 地址，例如 `100.64.10.20:18001`。该模式不生成本机 app service。`listen` 和 `upstream` 必须二选一；`upstream` 模式自动需要 Tailscale client。`upstream` 只适合 HTTP/WebSocket 服务，不用于 PostgreSQL、Redis、MySQL 等数据库端口公网发布。

默认 app 配置会启用 `nginx.http2: true`。目标机的 Nginx 必须至少为 `1.25.1` 且包含 `http_v2` 模块；如果使用 Debian/Ubuntu 发行版自带的较旧 Nginx，先把 app 配置里的 `nginx.http2` 改成 `false` 再部署。

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
api_version: meshify/app/v1alpha1

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

tailscale:
  # 如果同目录没有主 meshify.yaml，就显式填写 login_server。
  # login_server: "https://hs.example.com"
  # auth_key_file: "/etc/meshify/tailscale/app-auth-key"
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

日志路径建议默认保持 `nginx.access_log: ""`；只有需要接入既有日志目录、外部采集器或自定义轮转策略时，才显式设置其它路径。

| 配置 | 行为 | 是否自动轮转 |
| --- | --- | --- |
| `nginx.access_log: ""` + GoAccess 关闭 | Meshify 不渲染 app 专属 `access_log`，Nginx 继承全局日志配置；Debian/Ubuntu 通常使用 `/var/log/nginx/access.log` | Meshify 不处理；通常由发行版 Nginx/logrotate 处理 |
| `nginx.access_log: ""` + GoAccess 开启 | Meshify 使用并创建 `/var/log/meshify/apps/<app-name>/access.log`，并渲染 GoAccess 需要的日志格式 | 会自动轮转：daily、保留 14 份、压缩，轮转后 reload Nginx 并 restart GoAccess |
| 显式设置其它 `nginx.access_log` 路径 + GoAccess 关闭 | Meshify 只把路径写进 Nginx 配置，不创建文件或目录 | 不会；需要自行配置 logrotate |
| 显式设置其它 `nginx.access_log` 路径 + GoAccess 开启 | 该路径作为外部 canonical access log；部署前必须预先准备，并满足下方 GoAccess 安全要求 | 不会；需要自行轮转，并处理 Nginx reopen/reload 和 GoAccess 重启 |

`nginx.static_locations` 会渲染在 app 反代 location 之前，支持可选 `default_type`、`expires`、`Cache-Control`、`try_files $uri =404`、`gzip_static on` 和 `access_log off`。同一个 location 中建议只设置 `expires` 或 `cache_control` 之一，因为 Nginx `expires` 也会生成 `Cache-Control` 响应头；如果两者都设置，Meshify 会同时渲染两个指令。Meshify 不复制静态文件内容；静态文件应由业务发布流程和业务二进制一起放到对应 release 路径。

`nginx.http2` 为 true 或任一静态 location 设置 `gzip_static: true` 时，app deploy 会在写入 runtime 文件前检查 `nginx -V`；`http2 on;` 要求 Nginx 至少为 `1.25.1` 且包含 `http_v2` 模块。

#### GoAccess app 访问日志看板

`nginx.goaccess` 默认关闭。启用后会得到一个带 basic auth 的实时 HTML 访问日志看板，默认入口是 `https://<primary-domain>/_meshify/apps/<app-name>/goaccess`。Meshify 会安装或检查 `goaccess`，生成 GoAccess 配置、`<app-name>-goaccess.service`、`/var/lib/<app-name>/goaccess/report.html` 报表，以及启用 `persist true` 和 `restore true` 的 GoAccess `db-path` `/var/lib/<app-name>/goaccess/db`；如果使用 Meshify 托管的 access log，也会一起配置 `logrotate`。

启用 GoAccess 后始终是实时看板：Nginx 托管 HTML 报表，GoAccess service 通过 loopback WebSocket 提供实时数据，Meshify 在 `nginx.goaccess.websocket_path` 反代这个通道。

```yaml
nginx:
  access_log: ""
  error_log: "/var/log/nginx/example-app.error.log"
  goaccess:
    enabled: true
    language: "zh-CN" # en | zh-CN
    log_format: "enhanced" # enhanced | combined
    auth_basic_user_file: "/etc/example-app/goaccess.htpasswd"
    auth_cidr_allowlist: [] # 可选 CIDR；basic auth 仍然必须通过
    # path: "/_meshify/apps/example-app/goaccess"
    # websocket_path: "/_meshify/apps/example-app/goaccess/ws"
    websocket_listen: "" # 通常留空；默认 127.0.0.1:<app-derived-port>
```

启用前需要先准备 htpasswd 文件。它必须是已存在的普通非空文件，至少包含一行 `user:hash` 凭据；该行的 user 和 hash 都不能包含空白字符。文件必须 root-owned、不能被 group 写入、不能被其他本机用户访问，并且 Nginx 运行用户能读取。每一层父目录都必须 root-owned、不能被 group/others 写入，并且允许 Nginx 运行用户进入。放在 `/etc/<app-name>` 下时只能使用直接路径 `/etc/<app-name>/goaccess.htpasswd`。

`auth_cidr_allowlist` 为空时只要求 basic auth；非空时 Nginx 会同时要求来源 IP 匹配 allowlist 且 basic auth 通过。它不是免密白名单，不会绕过密码校验。

Debian/Ubuntu 上可以用 `apache2-utils` 提供的 `htpasswd` 创建：

```bash
sudo apt update
sudo apt install -y apache2-utils
sudo install -d -o root -g root -m 0755 /etc/example-app
sudo htpasswd -c -m /etc/example-app/goaccess.htpasswd goaccess
sudo chown root:www-data /etc/example-app/goaccess.htpasswd
sudo chmod 0640 /etc/example-app/goaccess.htpasswd
```

首次创建文件才使用 `-c`；后续新增或修改用户时去掉 `-c`，否则会清空已有凭据：

```bash
# 新增或修改用户时不要加 -c，避免覆盖已有 htpasswd 文件。
sudo htpasswd -m /etc/example-app/goaccess.htpasswd another-user
```

Meshify 不提供可复制的 htpasswd 模板，也不要复用示例 hash；这个文件本质上是看板密码库，必须按部署现场生成。

GoAccess 还需要 `nginx.goaccess.language` 对应的系统 locale。Debian/Ubuntu 通常自带 `C.UTF-8`；使用 `language: "zh-CN"` 时，部署前需要生成 `zh_CN.UTF-8`：

```bash
sudo apt install -y locales
sudo sed -i 's/^# *zh_CN.UTF-8 UTF-8/zh_CN.UTF-8 UTF-8/' /etc/locale.gen
sudo locale-gen zh_CN.UTF-8
locale -a | grep -Ei '^(C|C\.utf8|zh_CN\.utf8|zh_CN\.UTF-8)$'
```

登录 shell 自身的 locale 也要有效。例如 `env` 显示 `LANG=en_US.UTF-8` 时，`locale -a` 必须包含 `en_US.utf8`；否则生成它，或用 `sudo update-locale LANG=C.UTF-8` 把主机默认值改成 `C.UTF-8`。Meshify 会用 `LANG=C LC_ALL=C` 运行 GoAccess 的 `--version`、`--help` 和兼容性探测，避免依赖操作者 SSH 会话的 locale；真正部署出来的 GoAccess systemd service 仍会使用 `nginx.goaccess.language` 选择的 UTF-8 locale。

关键规则：

| 项目 | 规则 |
| --- | --- |
| `nginx.access_log` | 启用 GoAccess 时不能设为 `off`；其它行为见上面的日志路径表 |
| 外部 `access_log` 安全要求 | 必须预先存在，普通非 symlink，owner 为 root 或 www-data，文件不能被 group/others 写入，父目录必须 root-owned 且不能被 group/others 写入；Meshify 不创建文件、不 chown 外部日志根、不安装托管 logrotate；deploy 会先创建或确认 GoAccess runtime identity，再做最终可读性检查 |
| 禁用路径 | 外部 access log 不要放在其它 app 的 `/var/log/meshify/apps/`、当前 app 下的嵌套路径、`/home`、`/root`、`/run/user`、`/tmp`、`/var/tmp` 或 `/var/log/nginx`；`/home`、`/root`、`/run/user`、`/tmp`、`/var/tmp` 会被 `ProtectHome=true` 或 `PrivateTmp=true` 隔离，`/var/log/nginx` 通常由发行版 logrotate 管理 |
| `error_log` | GoAccess 不解析它；它不能等于 canonical access log 或 htpasswd 文件，也不能放在 app/GoAccess 生成目录下 |
| `path` / `websocket_path` | 通常留空，由 `app.name` 派生；显式设置后同步更新书签和监控探测 |
| `websocket_listen` | 通常留空；显式设置时必须是 loopback IP literal，不能使用 `localhost`、wildcard 或冲突端口；其它 loopback IP literal 也有效 |
| `language` | `en` 使用 `C.UTF-8`；`zh-CN` 使用 `zh_CN.UTF-8` 显示 GoAccess UI，并保持 `LC_TIME=C.UTF-8` 解析日志；deploy 会检查 `locale -a`，语言只影响 GoAccess UI 文案，不改变原始日志字段 |
| `log_format` | `enhanced` 是默认值，包含 Host 和 request serving time；`combined` 是兼容模式，不含 serving-time 指标 |

看板只以 primary domain 为准；secondary domain 上的看板请求会重定向到 `https://<primary-domain><nginx.goaccess.path>`，WebSocket 请求会返回 `421`。设置了 `access_log: false` 的静态 location 不会进入 GoAccess。GoAccess 覆盖请求量、访客、URL、状态码、IP/Host、referrer、User-Agent、带宽、访问时间；`enhanced` 模式还包含 request serving time。

部署或刷新后检查：

```bash
meshify app verify --config meshify-apps/example-app.yaml
sudo nginx -t
systemctl status example-app-goaccess.service --no-pager --full
curl -I https://app.example.com/_meshify/apps/example-app/goaccess
```

常用排障命令：

```bash
# 查看 GoAccess 实时看板服务状态。
systemctl status <app-name>-goaccess.service --no-pager --full

# 查看 GoAccess 服务最近日志。
journalctl -u <app-name>-goaccess.service -e

# 实时跟踪 GoAccess 使用的 canonical access log。
tail -f <canonical-access-log>

# 实时跟踪 Nginx error log。
tail -f <error-log>

# listen 模式：查看本机业务 app service 日志。
journalctl -u <app-name>.service -e

# upstream 模式：从当前宿主机检查固定 tailnet upstream。
curl -I http://<app.upstream>
```

`<canonical-access-log>` 使用配置里的 `nginx.access_log`；如果为空，则使用 `/var/log/meshify/apps/<app-name>/access.log`。`<error-log>` 使用配置里的 `nginx.error_log`；如果为空，则查看 Nginx 默认 error log。

#### app verify 和状态

`meshify app verify` 是 app 流程的静态配置和模板检查，状态通过时 CLI 输出 `static-passed`。它会校验 app schema、模板渲染、Nginx Host/SNI guard、证书路径、systemd 计划、Tailscale 需求推导、启用时的 GoAccess dashboard/WebSocket runtime 文件和敏感值泄露；它不读取宿主机上的已部署文件、systemd 状态、证书 SAN、Nginx runtime、GoAccess 进程状态或 Tailscale 在线状态。`meshify app deploy` 也会执行同类静态检查。

主流程的 `meshify status` 读取主部署 checkpoint、activation history 和上次可恢复失败；app 首版没有独立 checkpoint store，因此不提供 `meshify app status`。

release binary 的 app runtime 模板唯一来源是 `deploy/templates/app/`，并由 `meshify app deploy` 自动渲染和安装。

#### app 部署前检查

- `app.domains` 都解析到当前云服务器，且不要复用主 Headscale 的 `server_url` 主机名。
- 对 app 站点，公网只通过 Nginx 开放 `80/tcp` 和 `443/tcp`，不开放 `18001` 这类 app 端口；如果同机也运行主 Headscale 部署，`3478/udp` 仍然需要留给 STUN。
- 同机也管理主 Headscale 时，`app.listen` 和 `nginx.goaccess.websocket_listen` 不要复用 Headscale metrics 端口。
- `listen` 模式业务二进制已经安装，且 `service.exec_start` 的第一个 token 是可执行文件绝对路径。
- 如设置 `service.env_file`，它必须指向 root-owned、root-only 文件。
- 如启用 `nginx.goaccess.enabled`，按上文准备 htpasswd；显式外部 `nginx.access_log` 必须已存在并满足外部日志安全要求。
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
- lego 离线模式要求 `advanced.lego_source.file_path` 指向匹配 `advanced.platform.arch` 的固定版本 lego v5.1.0 archive。
- 已由 lego v4 创建的证书数据会在签发或续期前自动迁移。迁移失败时，按错误里提示的 lego data path 检查权限或异常文件，然后重新运行 deploy。

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
