# Unagikuma

<p align="center"><img src="docs/images/IMG_8128.jpeg" alt="Unagikuma" width="240"></p>

**또 하나의 가동 상태 점검 도구: u(ptime) + nagi(os) + kuma(Uptime Kuma).**

**두 마리 토끼를 다 잡습니다:** 한 대에서 파일 하나로 바로 쓰고, 더 필요하면 다른 컴퓨터를 합류시켜 클러스터로 만드세요. 같은 프로그램, 옮길 것은 없습니다.

쉽고 간편한 셀프 호스팅 모니터링: 웹사이트나 서버가 다운되면 바로 알 수 있고, 알림 폭탄은 없습니다.

[English](README.md) · [中文](README.zh.md) · [हिन्दी](README.hi.md) · [Español](README.es.md) · [العربية](README.ar.md) · [Français](README.fr.md) · [বাংলা](README.bn.md) · [Português](README.pt.md) · [Русский](README.ru.md) · [Bahasa Indonesia](README.id.md) · [اردو](README.ur.md) · [Deutsch](README.de.md) · [日本語](README.ja.md) · [Tiếng Việt](README.vi.md) · **한국어**

![페이퍼(흰색) 톤의 모니터 목록](docs/images/monitors-paper.png)

## Unagikuma를 써야 하는 이유

- **약 30초면 준비 완료.** 파일 하나를 내려받아 실행하고 브라우저를 열면 끝입니다.
- **파일 하나, 따로 설치할 것 없음.** Linux, macOS, Windows, FreeBSD에서 동작합니다.
- **중요한 것을 확인합니다.** 웹사이트, 서버, 포트, ping, DNS, SSL 인증서, 도메인 만료일.
- **늘 쓰는 곳으로 알림.** 이메일, Telegram, Slack, Discord, Feishu, DingTalk, WeCom, 웹훅 등.
- **상태 페이지**로 사용자에게 언제나 상황을 알려 줍니다.
- **옮겨 오기 쉽습니다.** Uptime Kuma, Nagios, Checkmate, Gatus의 모니터를 웹 화면이나 명령줄에서 가져올 수 있습니다.
- **내 언어로.** 화면은 15개 언어를 지원합니다.

## 빠른 시작

**1.** [Releases](https://github.com/githubflyideas/Unagikuma/releases/latest)에서 내 시스템에 맞는 파일을 내려받아 압축을 풉니다.

**2.** 실행합니다:

```sh
./unagikuma
```

Windows에서는 `unagikuma.exe`를 실행합니다.

**3.** 브라우저에서 http://localhost:8077 을 열고 관리자 계정을 만듭니다.

**4.** **모니터 추가**를 누르고 웹사이트 주소를 입력하면 끝입니다.

> 다른 컴퓨터에서 여나요? `http://<서버-주소>:8077`에 접속해 프로그램이 처음 시작할 때 보여 준 **설정 토큰**을 입력하세요.

## 한 대, 두 대, 클러스터

- **한 대:** 파일을 실행하고 모니터를 추가하면 끝. 데이터베이스도 에이전트도 필요 없습니다.
- **두 대:** **운영 모드** 에서 참여 코드를 붙여 넣으면 됩니다. 각자 검사하고 알리므로 어느 한쪽이 멈춰도 알림은 끊기지 않습니다.
- **클러스터:** 사이트를 원하는 만큼 추가하세요. 모든 알림에 사이트별 결과가 함께 표시되어 "모든 곳에서 다운"인지 "여기서만 다운"인지 알 수 있습니다.

![장애 중인 모니터와 사이트별 결과](docs/images/detail-sites.png)

## 알림 폭주 없음

- **내 네트워크가 끊겼다면?** 모니터마다가 아니라 네트워크에 대한 메시지 하나만 보냅니다.
- **호스트가 다운됐다면?** 그 위의 서비스를 하나하나 알리지 않습니다.
- **채널마다 1분에 최대 3건,** 나머지는 요약 하나로 옵니다. 복구 알림은 항상 즉시 나갑니다.

## 다른 도구에서 옮겨 오기

웹 화면에서 **설정 파일 › 가져오기** 를 열고 Uptime Kuma, Nagios, Checkmate, Gatus 중 하나를 고른 뒤 파일을 올리고, 미리 보기를 확인한 다음 추가하세요.

![웹 화면에서 Uptime Kuma, Nagios, Checkmate, Gatus 가져오기](docs/images/import.png)

명령줄에서도 됩니다. 먼저 `--dry-run` 으로 확인하고(아무것도 바뀌지 않음), 괜찮으면 빼고 같은 명령을 실행하세요.

```sh
./unagikuma import kuma      --dry-run /path/to/kuma.db
./unagikuma import nagios    --dry-run /etc/nagios4/nagios.cfg
./unagikuma import checkmate --dry-run monitors.json
./unagikuma import gatus     --dry-run config.yaml
```

Checkmate와 Gatus는 HTTP 검사를 옮깁니다. 그대로 옮길 수 없는 항목은 목록에 표시되고 가져오기를 멈추므로, 절반만 가져오는 일은 없습니다.

## 비밀번호를 잊으셨나요?

Unagikuma가 실행 중인 컴퓨터에서:

```sh
./unagikuma reset-password admin
```

새 비밀번호를 두 번 입력합니다(화면에 표시되지 않음). 대신 무작위 비밀번호를 받으려면 `--generate`를 붙이세요.

## 더 알아보기

- [사용자 설명서(영어)](docs/manual.md) — 나머지 모든 내용을 단계별로
- [변경 내역](CHANGELOG.md)

## 라이선스

Apache License 2.0, [LICENSE](LICENSE)를 참고하세요.
