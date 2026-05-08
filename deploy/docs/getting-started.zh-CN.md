# Meshify 中文入门

这份文档面向第一次接触 Headscale、Tailscale、MagicDNS、ACME 证书和 Nginx 的使用者。它说明 Meshify 会做什么、部署前要准备什么、配置文件怎么填，以及部署后如何让客户端加入私有网络。

## Meshify 是什么

Meshify 是一个 Go 写的命令行部署工具。它不是一个 Web 后台，也不是 Docker 或 Kubernetes 项目。

它的目标是把一台 Debian 13 或 Ubuntu 24.04 LTS 云服务器配置成一台单机 Headscale 控制面：

- Headscale 负责管理你的私有 Tailscale 网络。
- Nginx 负责公网 `80/tcp` 和 `443/tcp` 入口。
- lego 负责申请和续期 HTTPS 证书。
- Headscale 内置 DERP/STUN；STUN 帮助客户端做 UDP/NAT 探测，DERP 用于客户端无法直连时的 fallback。
- 客户端使用 Tailscale 官方客户端加入这个私有网络。

常规流程只有三步：

```bash
./meshify init --config meshify.yaml
./meshify deploy --config meshify.yaml
./meshify verify --config meshify.yaml
```

也可以用 `status` 查看部署上下文：

```bash
./meshify status --config meshify.yaml
```

## 部署前准备

目标服务器需要满足：

- Debian 13 或 Ubuntu 24.04 LTS。
- systemd、apt-get、dpkg 可用。
- root 或免密 sudo 权限。
- 架构是 `amd64` 或 `arm64`。
- 云安全组和本机防火墙放行 `80/tcp`、`443/tcp`、`3478/udp`。
- 有一个公网域名最终解析到这台服务器，例如 `hs.example.com`。
- 服务器能下载 Headscale `v0.28.0` 的 `.deb` 包，或者你准备了镜像/离线包。
- 服务器能下载 pinned lego `v4.35.2` GitHub release，或者你准备了离线
  archive/代理。
- 至少准备两台不同网络环境下的客户端做最终验证。

如果服务器在中国大陆公开提供服务，还需要提前确认 ICP/云厂商公网访问规则、云安全组、HTTP-01 是否能通，以及 GitHub/package 下载是否需要代理、镜像或离线包。

## 配置文件和内置模板

`meshify.yaml` 是运行时配置文件。它不需要在编译时写进二进制。

`deploy/config/meshify.yaml.example` 是仓库里的示例配置，用来告诉你字段长什么样。真正生效的是你传给 `--config` 的文件：

```bash
./meshify deploy --config meshify.yaml
```

编译进 `meshify` 二进制的是 `deploy/` 里的文档和运行时模板，例如 Headscale 配置模板、Nginx 配置模板、systemd timer 模板。你的实际 `meshify.yaml` 仍然是在服务器运行时读取的。

## init 做什么

`init` 只生成配置文件，不会安装服务，也不会修改系统。

```bash
./meshify init --config meshify.yaml
```

交互模式会询问：

- Headscale server URL。
- MagicDNS base domain。
- 证书邮箱。
- 是否进入高级模式。

交互模式生成的配置文件权限是 `0600`，因为后续高级配置可能涉及敏感路径或环境文件。如果目标文件已经存在，`init` 不会覆盖它。

非交互生成示例配置可以用：

```bash
./meshify init --example --config meshify.yaml
```

`--example` 只写入示例值，例如 `https://hs.example.com` 和 `tailnet.example.com`。你必须手动改成自己的域名、邮箱和部署参数。示例配置文件权限是 `0644`，因为它不应包含真实 secret。

## deploy 做什么

`deploy` 会读取 `meshify.yaml`，然后真正修改服务器。

```bash
./meshify deploy --config meshify.yaml
```

它按顺序执行：

1. 检查配置、系统版本、权限、DNS、端口、防火墙、服务冲突、包源、ACME 前置条件。
2. 安装主机依赖：`nginx`、`ca-certificates`、`curl`、`tar`、`openssl`。
3. 下载或使用离线 lego `v4.35.2` archive，校验 SHA-256 后安装到
   `/opt/meshify/bin/lego`。
4. 下载或使用离线 Headscale `v0.28.0` `.deb` 包，校验 SHA-256 后安装。
5. 渲染并写入运行时文件：
   - `/etc/headscale/config.yaml`
   - `/etc/headscale/policy.hujson`
   - `/etc/nginx/sites-available/headscale.conf`
   - `/etc/systemd/system/meshify-lego-renew.service`
   - `/etc/systemd/system/meshify-lego-renew.timer`
   - `/usr/local/lib/meshify/hooks/install-lego-cert-and-reload-nginx.sh`
6. 用 lego 申请证书。
7. 启用 Headscale、Nginx 和证书续期 timer，启动续期 timer，重启 Headscale，并 reload Nginx 配置。
8. 创建默认 `meshify` 用户和一个 24 小时 preauth key。
9. 执行静态验证。

如果中途失败，Meshify 会在配置文件旁边的 `.meshify/` 目录保存 checkpoint。修复问题后重复执行同一条 `deploy` 命令即可。

## root 和 sudo 权限

部署会写 `/etc`、安装 apt 包、控制 systemd，所以需要 root 权限。

最简单的方式：

```bash
sudo ./meshify deploy --config meshify.yaml
```

如果你不想每次加 `sudo`，当前用户必须有免密 sudo。可以先测试：

```bash
sudo -n true
```

这个命令能成功，Meshify 才能在需要时自动使用 sudo。需要输入密码的 sudo 不适合作为非交互部署权限。

## server_url

`server_url` 是客户端连接 Headscale 控制面的公网 HTTPS 地址。

示例：

```yaml
default:
  server_url: "https://hs.example.com"
```

它必须是真实公网域名，并且 DNS 需要最终解析到这台云服务器的公网地址。可以是 A/AAAA 记录，也可以是最终解析到公网地址的 CNAME 链。

客户端加入私有网络时会使用这个地址：

```bash
sudo tailscale up \
  --login-server https://hs.example.com \
  --auth-key <preauth-key> \
  --accept-dns=true
```

它不是 Web 管理后台地址。默认管理操作仍然通过服务器本地 Headscale CLI：

```bash
sudo headscale --config /etc/headscale/config.yaml users list
sudo headscale --config /etc/headscale/config.yaml nodes list
```

## base_domain 和 MagicDNS

`base_domain` 是 MagicDNS 的私有域名后缀。

示例：

```yaml
default:
  base_domain: "tailnet.example.com"
```

MagicDNS 可以理解成私有网络里的自动 DNS。设备加入网络后，可以用设备名访问，而不是记 Tailnet IP。

例如设备名是：

- `laptop`
- `nas`
- `phone`

`base_domain` 是：

```text
tailnet.example.com
```

那么客户端内部可以解析：

```text
laptop.tailnet.example.com
nas.tailnet.example.com
phone.tailnet.example.com
```

这些名字通常不靠公网 DNS 服务商解析，而是由 Headscale/Tailscale 客户端在私有网络内部处理。

推荐使用你自己控制域名下面的子域名：

```yaml
server_url: "https://hs.example.com"
base_domain: "tailnet.example.com"
```

`base_domain` 不需要配置公网 A 记录或 CNAME。它主要是一个私有命名空间。不过不要用别人的真实域名，也不要照抄 `example.com` 到生产环境。

当前配置校验要求：

- `base_domain` 不能等于 `server_url` 的主机名。
- `base_domain` 不能是 `server_url` 主机名的父域名。

这样可以：

```yaml
server_url: "https://hs.example.com"
base_domain: "tailnet.example.com"
```

这样不可以：

```yaml
server_url: "https://hs.example.com"
base_domain: "example.com"
```

## 设备名和 Tailnet IP

设备加入后，Headscale 会自动分配 Tailnet IP。当前模板默认使用：

```text
100.64.0.0/10
```

这些 IP 不是云厂商 VPC 内网 IP，而是 Tailscale/Headscale 虚拟网络 IP。一般不需要手动设置。

在客户端查看：

```bash
tailscale ip -4
tailscale status
```

在服务器查看节点：

```bash
sudo headscale --config /etc/headscale/config.yaml nodes list
```

设备 DNS 名字通常来自系统 hostname。Linux 客户端加入私有网络：

```bash
sudo tailscale up \
  --login-server https://hs.example.com \
  --auth-key <preauth-key> \
  --accept-dns=true
```

加入后可以修改设备名：

```bash
sudo tailscale set --hostname=laptop
```

不依赖 MagicDNS 时，也可以直接用 Tailnet IP 访问，例如：

```bash
ssh user@100.64.1.23
```

## HTTP-01 和 DNS-01

HTTP-01 和 DNS-01 是 ACME 证书验证方式。两者选一个。

默认推荐 HTTP-01：

```yaml
default:
  acme_challenge: "http-01"
```

HTTP-01 的原理是：证书机构访问你的公网 HTTP 地址，确认你控制这个域名。因此要求：

- `server_url` 的域名解析到这台服务器。
- 公网 `80/tcp` 能访问这台服务器。
- CDN、WAF、反向代理没有拦截 `/.well-known/acme-challenge/`。

DNS-01 的原理是：程序通过 DNS 服务商 API 创建 TXT 记录，证明你控制这个域名。它适合：

- ACME 证书验证不能可靠依赖 HTTP-01。
- 域名前面有复杂 CDN/代理。
- 组织策略要求不用 HTTP 验证。
- 需要通过 DNS API 自动完成证书签发。

DNS-01 配置更复杂，因为需要 DNS 服务商 API 凭据。注意：DNS-01 可以避免证书签发依赖 HTTP-01 challenge，但 Meshify 的默认运行时仍会配置 Nginx 监听 `80/tcp` 做 HTTP 入口和重定向；目标主机本地的 `80/tcp` 仍不能被其它服务冲突占用。

当前 Meshify 支持的 DNS-01 provider 是：

- `cloudflare`
- `route53`
- `digitalocean`
- `gcloud` 或 `google`

当前不支持腾讯云 DNS 或 GoDaddy DNS provider。域名可以在 GoDaddy 购买，但如果 DNS 仍托管在 GoDaddy，本项目的 DNS-01 不能直接使用。你可以选择：

- 使用 HTTP-01。
- 把 DNS 托管迁移到 Cloudflare 后使用 DNS-01。

## Cloudflare DNS-01 示例

如果你的域名 DNS 托管在 Cloudflare，可以使用 DNS-01。

配置示例：

```yaml
api_version: meshify/v1alpha1

default:
  server_url: "https://hs.example.com"
  base_domain: "tailnet.example.com"
  certificate_email: "ops@example.com"
  acme_challenge: "dns-01"

advanced:
  headscale_source:
    mode: "direct"
    version: "0.28.0"
    url: ""
    sha256: ""
    file_path: ""

  lego_source:
    mode: "direct"
    file_path: ""

  proxy:
    http_proxy: ""
    https_proxy: ""
    no_proxy: ""

  dns01:
    provider: "cloudflare"
    env_file: "/etc/meshify/dns01/cloudflare.env"

  network:
    public_ipv4: ""
    public_ipv6: ""

  platform:
    arch: "amd64"
```

在 Cloudflare 创建 API token 时，建议使用指定 zone 范围内的最小权限。按 Cloudflare API 权限名，对应 `DNS Write` 和 `Zone Read`；在控制台里通常显示为 Zone/DNS/Edit 和 Zone/Zone/Read。

在服务器上准备 token 文件：

```bash
sudo install -d -m 0700 /etc/meshify/dns01
sudo install -m 0600 /dev/null /etc/meshify/dns01/cloudflare-token
sudo nano /etc/meshify/dns01/cloudflare-token
```

把 Cloudflare API token 写入 `cloudflare-token`。

再创建 env 文件：

```bash
sudo install -m 0600 /dev/null /etc/meshify/dns01/cloudflare.env
sudo nano /etc/meshify/dns01/cloudflare.env
```

内容：

```bash
CF_DNS_API_TOKEN_FILE=/etc/meshify/dns01/cloudflare-token
```

不要把 token 直接写进 `meshify.yaml`，也不要把 raw token 直接写进 `cloudflare.env`。当前项目要求使用 lego 支持的 `_FILE` 变量，把真实 secret 放在 root-only 文件里。

## Headscale 和 lego 离线包

如果云服务器访问 GitHub release 很慢或经常超时，可以先在本地下载文件，再
上传到服务器。

Headscale 离线 `.deb` 使用 `advanced.headscale_source`：

```yaml
advanced:
  headscale_source:
    mode: "offline"
    version: "0.28.0"
    url: ""
    sha256: "<headscale-deb-sha256>"
    file_path: "/srv/meshify/headscale_0.28.0_linux_amd64.deb"
```

如果你手里已有旧配置，原来的 `advanced.package_source` 需要改名为
`advanced.headscale_source`，内部字段不变。

`sha256` 填服务器上实际文件的 SHA-256：

```bash
sha256sum /srv/meshify/headscale_0.28.0_linux_amd64.deb
```

lego 离线 `.tar.gz` 使用 `advanced.lego_source`：

```yaml
advanced:
  lego_source:
    mode: "offline"
    file_path: "/srv/meshify/lego_v4.35.2_linux_amd64.tar.gz"
```

lego 不需要在配置里填写 SHA-256。Meshify 已经内置了当前发布锁定的 lego
SHA-256，并会在安装前校验本地文件。当前内置值是：

```text
amd64: ee5be4bf457de8e3efa86a51651c75c87f0ee0e4e9f3ae14f6034d68365770f3
arm64: e1f153179098d27ce044aaaa168c0e323d50ae71b0f1a147aa8ae49ac6b14d89
```

确保 `advanced.platform.arch` 和你上传的文件架构一致：

```yaml
advanced:
  platform:
    arch: "amd64"
```

离线模式只改变软件包来源，不改变安装结果。Meshify 仍会把 lego 安装到
`/opt/meshify/bin/lego`，把 Headscale 安装成系统包，并继续执行证书、Nginx
和 systemd 部署流程。

## Nginx 已存在时

Meshify 会执行：

```bash
apt-get install -y nginx ca-certificates curl tar openssl
```

如果 Nginx 没安装，会安装。如果已经安装，apt 会按当前系统软件源判断是否需要安装或升级。Meshify 不会主动指定 Nginx 版本，也不会主动把新版本回退到旧版本。

真正需要注意的是配置共存。

Meshify 会写入自己的 Nginx site，并启用：

```text
/etc/nginx/sites-available/headscale.conf
/etc/nginx/sites-enabled/headscale.conf
```

同时会移除发行版默认站点 symlink，并执行：

```bash
nginx -t
systemctl reload nginx.service
```

如果服务器已有网站也使用 Nginx，可以共存，但需要确认：

- 其它网站使用不同的 `server_name`。
- 没有其它站点抢占 `hs.example.com`。
- 没有冲突的 `default_server`。
- HTTP-01 时，`hs.example.com/.well-known/acme-challenge/` 能进入 Meshify 管理的 Nginx 配置。
- `nginx -t` 能通过。

如果已有 Apache、Caddy、Traefik 等其它 Web 服务占用 `80/443`，Meshify 会把它们视为阻塞冲突。新手最稳妥的方式是给 Meshify 使用一台干净的云服务器。

## 最小 HTTP-01 部署示例

假设你有域名：

```text
example.com
```

准备用：

```text
hs.example.com
```

作为 Headscale 公网入口。

1. 在 DNS 服务商处添加记录：

```text
hs.example.com  A  <云服务器公网 IPv4>
```

如果你有 IPv6，也可以加 AAAA 记录。

2. 在云安全组放行：

```text
80/tcp
443/tcp
3478/udp
```

3. 把 `meshify` 二进制放到服务器上。

4. 生成配置：

```bash
./meshify init --config meshify.yaml
```

5. 按提示填写：

```text
Headscale server URL: https://hs.example.com
MagicDNS base domain: tailnet.example.com
Certificate email: ops@example.com
```

默认选择 HTTP-01 即可。

6. 部署：

```bash
sudo ./meshify deploy --config meshify.yaml
```

7. 验证：

```bash
./meshify verify --config meshify.yaml
./meshify status --config meshify.yaml
```

8. 记录 deploy 输出里的 preauth key。

## 客户端加入

客户端需要 Tailscale client `>= v1.74.0`。

Linux 示例：

```bash
sudo tailscale up \
  --login-server https://hs.example.com \
  --auth-key <preauth-key> \
  --accept-dns=true
```

如果需要把这台客户端命名为 `laptop`，加入后执行：

```bash
sudo tailscale set --hostname=laptop
```

查看状态：

```bash
tailscale status
tailscale ip -4
tailscale netcheck
```

测试另一台设备：

```bash
tailscale ping nas
tailscale ping nas.tailnet.example.com
```

建议至少用两台不同网络环境下的客户端验证，例如家宽和手机热点。

## 生成新的 preauth key

`deploy` 会创建初始 preauth key。如果过期了，可以在服务器上重新创建：

```bash
sudo headscale --config /etc/headscale/config.yaml users list
sudo headscale --config /etc/headscale/config.yaml preauthkeys create --user <ID> --expiration 24h
```

`<ID>` 使用 `users list` 输出里 `meshify` 用户对应的数字 ID。

## 常见选择

新手默认推荐：

```yaml
default:
  server_url: "https://hs.example.com"
  base_domain: "tailnet.example.com"
  certificate_email: "ops@example.com"
  acme_challenge: "http-01"
```

只有在公网 80 不可用、必须通过 DNS API 验证证书、或组织策略要求时，再考虑 DNS-01。

如果服务器上已有生产 Nginx 网站，建议先不要直接部署到同一台机器。先用一台干净测试服务器跑通 Meshify，再评估 Nginx 共存。
