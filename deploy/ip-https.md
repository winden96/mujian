# IP HTTPS 与登录会话

## Planner

2026-09-13 修复生产 HTTP 登录后立即过期的问题。生产 `sessionCookieOptions()` 设置 Secure Cookie；HTTP 登录返回成功，但浏览器不保存该 Cookie，后续 `/api/user/self` 返回 401。公网 Chromium 已复现 Cookie 数量为 0，切换页面跳到 `/login?expired=true`。`www.mujianai.com` 公网返回云端 ADM 403，IP 的 HTTP-01 验证路径可达。

架构检查：你是一名高级架构开发工程师，能精确的知道代码Bug，禁止胡乱兜底糊弄，要切入到具体问题，以及从底层的角度思考问题。给出最优的解决方案。

修复部署入口，保留生产 Secure、HttpOnly、SameSite=Strict 及原 session secret。应用代码、镜像和数据库结构不变，Seedance 2.5 不在范围内。

## Generator

- 浏览器地址：`https://1.92.114.137`。旧 `http://1.92.114.137:10088` 的首页及 HTML 导航返回 308，保留路径和查询参数。
- Nginx 使用 `nginx/mujian-ip.conf`，监听 80、10088 和 TLS 443。旧 HTTP API 继续代理，避免改变现有客户端方法、鉴权或请求体；客户端可逐步改用 HTTPS。
- 应用只映射 `127.0.0.1:10090:3000`。生产原有 `/opt/mujian/runtime/docker-compose.public-port.yml` 使用 `docker-compose.ip-https.yml` 内容，后续发布必须继续传入该覆盖文件。
- Certbot 5.4.0 安装于独立 venv `/opt/mujian/certbot-ip/venv`，与系统 Certbot 2.9 分离。IP 证书使用独立 `/etc/letsencrypt-ip`、`/var/lib/letsencrypt-ip`、`/var/log/letsencrypt-ip`。
- 证书采用 Let's Encrypt `shortlived` profile，通过 webroot `/var/www/certbot` 签发。正式证书路径 `/etc/letsencrypt-ip/live/1.92.114.137/`。
- `mujian-ip-cert-renew.timer` 每 6 小时检查，随机延迟最多 30 分钟，支持补执行。成功续期通过 `renew_hook=/opt/mujian/certbot-ip/reload-nginx.sh` 先验证 Nginx，再 reload。
- 安装入口 `scripts/configure-ip-https.sh <current-release-directory> <existing-backup-directory>`。需要已经签发的可信 IP 证书和备份，先校验证书、配置，切换失败恢复代理与端口覆盖。

IP 证书签发命令（Certbot >=5.4，先使用独立 staging 配置目录验证，再签正式证书）：

```sh
/opt/mujian/certbot-ip/venv/bin/certbot certonly \
  --preferred-profile shortlived --webroot --webroot-path /var/www/certbot \
  --ip-address 1.92.114.137 --cert-name 1.92.114.137 \
  --config-dir /etc/letsencrypt-ip --work-dir /var/lib/letsencrypt-ip \
  --logs-dir /var/log/letsencrypt-ip
```

参考：[Let's Encrypt IP 证书说明](https://letsencrypt.org/2026/03/11/shorter-certs-certbot)、[浏览器 Secure Cookie 规则](https://developer.mozilla.org/en-US/docs/Web/HTTP/Reference/Headers/Set-Cookie)。

## Evaluator

按 code-simplify → code-review → 验证顺序执行，重点检查代理鉴权头、请求方法、流式配置、私钥路径、续期重载和回滚。无关闭 Secure Cookie、忽略 401 或修改用户密码的兜底。

- `bash -n`、`git diff --check`、增量 Gitleaks：通过。
- Nginx 配置校验、systemd unit/timer 校验：通过。
- staging 和正式 IP 证书签发：通过。正式证书的 IP SAN、信任链和有效期校验：通过。
- Certbot `reconfigure --run-deploy-hooks` 模拟续期及 Nginx reload：通过；续期 timer 启用，实际 service 执行成功。
- 公网 HTTPS `/api/status`：200，证书验证结果 0；旧 HTTP HTML 导航：308 到相同路径的 HTTPS 地址。
- 旧 HTTP `/api/status` 仍为 200；新旧 `/v1/models` 无令牌均正确返回 401，鉴权未放宽。
- 浏览器具体登录、页面切换及刷新结果见外部发布产物 `mujian-release-artifacts/ip-https-20260913/acceptance.md`。

本次没有修改前端或后端应用源码，复用 obs.10 已通过构建和测试的镜像。验证重点是公网浏览器实际 `UI → API → Cookie → UI` 会话链路及代理配置，而非重复构建相同应用。

## Artifact

应用仍为 `v0.12.14-obs.10` / `b153e51ee5ccd00e3caeaaf527b3f2c9aadd5bb5`。配置备份位于 `/opt/mujian/backups/ip-https-20260913/`，部署产物位于 `/opt/mujian/build-artifacts/ip-https-20260913/`。

需要回滚入口时：停用新增续期 timer，恢复备份的 Nginx 配置并校验/reload，释放 10088 监听，再恢复备份的 Compose 端口覆盖并仅重建 app。应用和数据库无需回滚。回到旧 HTTP 配置也会重新出现本次登录问题，因此只用于部署故障处置。

域名的云端 403 仍需单独处理。IP 证书有效期短，保留自动续期及 webroot 验证入口；不要手工禁用 timer。只新增这一条 IP 入口，未修改支付、OBS、渠道和计费规则。
