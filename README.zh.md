# Unagikuma

<p align="center"><img src="docs/images/IMG_8128.jpeg" alt="Unagikuma" width="240"></p>

**又一个可用性监控工具:u(ptime) + nagi(os) + kuma(Uptime Kuma)。**

**鱼和熊掌,可以兼得:** 一台机器、一个文件,开箱即用;想要更多,把其他机器加进来就成了集群。同一个程序,不用迁移。

简单好用的自托管监控:网站和服务器一出问题,马上就知道,而且不吵人。

[English](README.md) · **中文** · [हिन्दी](README.hi.md) · [Español](README.es.md) · [العربية](README.ar.md) · [Français](README.fr.md) · [বাংলা](README.bn.md) · [Português](README.pt.md) · [Русский](README.ru.md) · [Bahasa Indonesia](README.id.md) · [اردو](README.ur.md) · [Deutsch](README.de.md) · [日本語](README.ja.md) · [Tiếng Việt](README.vi.md) · [한국어](README.ko.md)

![纸白色调下的监控列表](docs/images/monitors-paper.png)

## 为什么选择 Unagikuma

- **大约 30 秒就能用上。** 下载一个文件,运行它,打开浏览器即可。
- **只有一个文件,不用装别的。** 支持 Linux、macOS、Windows 和 FreeBSD。
- **该查的都能查。** 网站、服务器、端口、Ping、DNS、SSL 证书,以及域名到期时间。
- **告警发到你常用的地方。** 邮件、Telegram、Slack、Discord、飞书、钉钉、企业微信、Webhook 等。
- **状态页**,让你的用户随时了解服务状态。
- **轻松迁移。** 可以把 Uptime Kuma、Nagios、Checkmate 或 Gatus 里的监控搬过来,网页上或命令行都行。
- **七种主题,也可以用自己的。** 从白到黑五种色调(纸白、雾灰、石板灰、石墨、墨黑),外加天空蓝和紫罗兰,配上 Nagios 式的状态颜色;上传主题包换成自己的配色,注册版还能换成自己的产品名和 logo。
- **说你的语言。** 界面支持 15 种语言。

## 快速上手

**1.** 从 [Releases](https://github.com/githubflyideas/Unagikuma/releases/latest) 下载适合你系统的文件并解压。

**2.** 运行:

```sh
./unagikuma
```

Windows 上运行 `unagikuma.exe`。

**3.** 在浏览器打开 http://localhost:8077,创建管理员账号。

**4.** 点击 **添加监控项**,填入网址,就完成了。

> 从另一台电脑打开?访问 `http://<服务器地址>:8077`,并输入程序首次启动时显示的 **初始化令牌**。

## 单机、双机、集群

- **单机:** 运行文件,加一个监控项,完成。不用数据库,不用代理。
- **双机:** 在 **运行模式** 里粘贴加入码即可。两台各自检测、各自告警,任何一台宕机,告警都不会断。
- **集群:** 想加多少站点都行。每条告警都附上各站点看到的结果,一眼分清"全网都挂了"还是"只是这里连不上"。

![一个故障监控项,以及每个站点看到的结果](docs/images/detail-sites.png)

## 不刷屏

- **自己的网络断了?** 只发一条关于网络的消息,而不是每个监控项一条。
- **主机宕机?** 它上面的服务不会一个个分别报警。
- **每个通道每分钟最多三条告警,** 其余汇成一条摘要。恢复通知总是立即发出。

## 从其他工具迁移

在网页上打开 **配置 › 导入**,选 Uptime Kuma、Nagios、Checkmate 或 Gatus,上传文件,看一眼预览,确认添加即可。

![在网页上从 Uptime Kuma、Nagios、Checkmate 或 Gatus 导入](docs/images/import.png)

也可以用命令行:先加 `--dry-run` 预览(不做任何改动),确认没问题后去掉它再运行一次。

```sh
./unagikuma import kuma      --dry-run /path/to/kuma.db
./unagikuma import nagios    --dry-run /etc/nagios4/nagios.cfg
./unagikuma import checkmate --dry-run monitors.json
./unagikuma import gatus     --dry-run config.yaml
```

Checkmate 和 Gatus 迁移的是 HTTP 检测。无法原样迁移的项目会列出来并阻止导入,不会只导入一半。

## 忘记密码？

在运行 Unagikuma 的机器上执行：

```sh
./unagikuma reset-password admin
```

输入两次新密码（不会显示）。加上 `--generate` 可以改为自动生成一个随机密码。

## 了解更多

- [用户手册(英文)](docs/manual.md) — 其他所有内容,一步一步讲清楚;完整手册只有英文版
- [更新日志](CHANGELOG.md)

## 许可证

Apache License 2.0,详见 [LICENSE](LICENSE)。
