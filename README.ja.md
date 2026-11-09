# Unagikuma

<p align="center"><img src="docs/images/IMG_8128.jpeg" alt="Unagikuma" width="240"></p>

**もうひとつの死活監視ツール:u(ptime) + nagi(os) + kuma(Uptime Kuma)。**

**二兎を追って二兎を得る：** 1 台のマシンでファイル 1 つ、すぐに使えます。もっと必要になったら他のマシンを参加させればクラスターに。同じプログラムで、移行は不要です。

かんたんなセルフホスト型監視。サイトやサーバーが落ちたらすぐに気づけて、通知でうるさくなりません。

[English](README.md) · [中文](README.zh.md) · [हिन्दी](README.hi.md) · [Español](README.es.md) · [العربية](README.ar.md) · [Français](README.fr.md) · [বাংলা](README.bn.md) · [Português](README.pt.md) · [Русский](README.ru.md) · [Bahasa Indonesia](README.id.md) · [اردو](README.ur.md) · [Deutsch](README.de.md) · **日本語** · [Tiếng Việt](README.vi.md) · [한국어](README.ko.md)

![ペーパー（白）トーンの監視一覧](docs/images/monitors-paper.png)

## Unagikuma が選ばれる理由

- **約 30 秒で使い始められます。** ファイルを 1 つダウンロードして実行し、ブラウザーを開くだけ。
- **ファイルは 1 つだけ。ほかに入れるものはありません。** Linux、macOS、Windows、FreeBSD で動きます。
- **大事なところをしっかり監視。** Web サイト、サーバー、ポート、Ping、DNS、SSL 証明書、ドメインの有効期限。
- **いつも使っている場所に通知。** メール、Telegram、Slack、Discord、Feishu、DingTalk、WeCom、Webhook など。
- **ステータスページ**で、利用者にいつでも状況を伝えられます。
- **乗り換えもかんたん。** Uptime Kuma、Nagios、Checkmate、Gatus から、Web 画面でもコマンドラインでも監視を取り込めます。
- **あなたの言語で。** 画面は 15 の言語に対応しています。

## クイックスタート

**1.** [Releases](https://github.com/githubflyideas/Unagikuma/releases/latest) からお使いのシステム用のファイルをダウンロードして展開します。

**2.** 実行します:

```sh
./unagikuma
```

Windows では `unagikuma.exe` を実行します。

**3.** ブラウザーで http://localhost:8077 を開き、管理者アカウントを作成します。

**4.** **監視を追加** をクリックし、Web サイトのアドレスを入力すれば完了です。

> 別のパソコンから開く場合は `http://<サーバーのアドレス>:8077` にアクセスし、初回起動時にプログラムが表示した **セットアップトークン** を入力してください。

## 1 台、2 台、クラスター

- **1 台：** ファイルを実行して監視を追加すれば完了。データベースもエージェントも不要です。
- **2 台：** **運用モード** で参加コードを貼り付けるだけ。それぞれが独立してチェックと通知を行うので、どちらかが止まっても通知は途切れません。
- **クラスター：** サイトはいくつでも追加できます。すべての通知に各サイトの結果が添えられ、「どこからもダウン」か「ここからだけダウン」かがわかります。

![障害中の監視と、サイトごとの結果](docs/images/detail-sites.png)

## アラートの洪水を防ぐ

- **自分のネットワークが落ちた？** 監視ごとではなく、ネットワークについて 1 通だけ送ります。
- **ホストが落ちた？** その上のサービスを 1 つずつ通知しません。
- **チャンネルごとに 1 分あたり最大 3 件。** 残りは 1 通のまとめで届きます。復旧通知は常にすぐ送られます。

## 他のツールからの移行

Web 画面で **設定ファイル › インポート** を開き、Uptime Kuma、Nagios、Checkmate、Gatus のいずれかを選んでファイルをアップロードし、プレビューを確認して追加します。

![Web 画面で Uptime Kuma、Nagios、Checkmate、Gatus から取り込む](docs/images/import.png)

コマンドラインでも可能です。まず `--dry-run` で確認し(何も変更されません)、問題なければ外して同じコマンドを実行します。

```sh
./unagikuma import kuma      --dry-run /path/to/kuma.db
./unagikuma import nagios    --dry-run /etc/nagios4/nagios.cfg
./unagikuma import checkmate --dry-run monitors.json
./unagikuma import gatus     --dry-run config.yaml
```

Checkmate と Gatus は HTTP チェックを移行します。そのまま移せない項目は一覧に出て取り込みを止めるので、中途半端に取り込まれることはありません。

## パスワードを忘れたら

Unagikuma が動いているマシンで実行します:

```sh
./unagikuma reset-password admin
```

新しいパスワードを 2 回入力します(画面には表示されません)。代わりにランダムなパスワードを作るには `--generate` を付けます。

## 通知・連絡先・待受

- メール/SMS は連絡先の在職者のみ。チャネルのテストも原則同じ名簿。
- 通知の失敗は数回再試行（`notify.channel-retries=0` で再試行なし）。プローブ再試行は別。
- UI は待受で平文 HTTP が既定。TLS リバプロか `--tls-cert`/`--tls-key` を推奨。エージェントは同一ポートでピン留め TLS。

## さらに詳しく

- [ユーザーマニュアル(英語)](docs/manual.md) — そのほかのすべてを順を追って説明
- [変更履歴](CHANGELOG.md)

## ライセンス

Apache License 2.0。詳しくは [LICENSE](LICENSE) をご覧ください。
