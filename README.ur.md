# Unagikuma

<p align="center"><img src="docs/images/IMG_8128.jpeg" alt="Unagikuma" width="240"></p>

<div dir="rtl">

**اپ ٹائم جانچنے کا ایک اور ٹول: u(ptime) + nagi(os) + kuma(Uptime Kuma)۔**

**ایک پنتھ دو کاج:** ایک مشین پر ایک فائل، فوراً تیار؛ مزید چاہیے تو دوسری مشینیں شامل کریں اور یہ کلسٹر بن جاتا ہے۔ وہی پروگرام، کچھ منتقل نہیں کرنا۔

آسان، اپنے سرور پر چلنے والی مانیٹرنگ: آپ کی ویب سائٹ یا سرور بند ہوتے ہی فوراً پتا چل جائے، بغیر فضول شور کے۔

</div>

[English](README.md) · [中文](README.zh.md) · [हिन्दी](README.hi.md) · [Español](README.es.md) · [العربية](README.ar.md) · [Français](README.fr.md) · [বাংলা](README.bn.md) · [Português](README.pt.md) · [Русский](README.ru.md) · [Bahasa Indonesia](README.id.md) · **اردو** · [Deutsch](README.de.md) · [日本語](README.ja.md) · [Tiếng Việt](README.vi.md) · [한국어](README.ko.md)

![کاغذ ٹون میں مانیٹرز کی فہرست](docs/images/monitors-paper.png)

<div dir="rtl">

## Unagikuma کیوں؟

- **تقریباً 30 سیکنڈ میں تیار۔** ایک فائل ڈاؤن لوڈ کریں، چلائیں اور براؤزر کھولیں۔
- **صرف ایک فائل، اور کچھ انسٹال نہیں کرنا۔** Linux، macOS، Windows اور FreeBSD پر چلتا ہے۔
- **ضروری چیزوں کی جانچ۔** ویب سائٹس، سرورز، پورٹس، پنگ، DNS، SSL سرٹیفکیٹ اور ڈومین کی میعاد۔
- **الرٹ وہیں جہاں آپ ہیں۔** ای میل، Telegram، Slack، Discord، Feishu، DingTalk، WeCom، ویب ہُکس اور بہت کچھ۔
- **اسٹیٹس پیجز** تاکہ آپ کے صارفین ہمیشہ باخبر رہیں۔
- **منتقلی آسان۔** Uptime Kuma، Nagios، Checkmate یا Gatus سے اپنے مانیٹر لائیں، ویب صفحے سے یا کمانڈ لائن سے۔
- **آپ کی زبان میں۔** انٹرفیس 15 زبانوں میں دستیاب ہے۔

</div>

<div dir="rtl">

## فوری آغاز

**1.** [Releases](https://github.com/githubflyideas/Unagikuma/releases/latest) سے اپنے سسٹم کے لیے فائل ڈاؤن لوڈ کریں اور اسے کھولیں (ان پیک کریں)۔

**2.** اسے چلائیں:

</div>

```sh
./unagikuma
```

<div dir="rtl">

Windows پر `unagikuma.exe` چلائیں۔

**3.** براؤزر میں http://localhost:8077 کھولیں اور اپنا ایڈمن اکاؤنٹ بنائیں۔

**4.** **مانیٹر شامل کریں** پر کلک کریں، کسی ویب سائٹ کا پتا لکھیں، بس ہو گیا۔

> کسی دوسرے کمپیوٹر سے کھول رہے ہیں؟ `http://<سرور-کا-پتا>:8077` پر جائیں اور وہ **سیٹ اپ ٹوکن** درج کریں جو پروگرام نے پہلی بار شروع ہوتے وقت دکھایا تھا۔

</div>

<div dir="rtl">

## کسی دوسرے ٹول سے منتقلی

ویب صفحے میں **کنفیگریشن › درآمد** کھولیں، Uptime Kuma، Nagios، Checkmate یا Gatus منتخب کریں، فائل اپ لوڈ کریں، پیش نظارہ دیکھیں اور شامل کریں۔

</div>

![ویب صفحے میں Uptime Kuma، Nagios، Checkmate یا Gatus سے امپورٹ](docs/images/import.png)

<div dir="rtl">

یا کمانڈ لائن سے: پہلے `--dry-run` کے ساتھ دیکھیں (کچھ نہیں بدلتا)، پھر وہی کمانڈ اس کے بغیر چلائیں۔

</div>

```sh
./unagikuma import kuma      --dry-run /path/to/kuma.db
./unagikuma import nagios    --dry-run /etc/nagios4/nagios.cfg
./unagikuma import checkmate --dry-run monitors.json
./unagikuma import gatus     --dry-run config.yaml
```

<div dir="rtl">

Checkmate اور Gatus: HTTP جانچیں منتقل ہوتی ہیں۔ جو چیز ہوبہو منتقل نہ ہو سکے وہ فہرست میں آتی ہے اور امپورٹ روک دیتی ہے، اس لیے کچھ بھی آدھا نہیں رہتا۔

</div>

<div dir="rtl">

## پاس ورڈ بھول گئے؟

جس مشین پر Unagikuma چل رہا ہے وہاں چلائیں:

</div>

```sh
./unagikuma reset-password admin
```

<div dir="rtl">

نیا پاس ورڈ دو بار لکھیں (یہ دکھائی نہیں دے گا)۔ اس کی جگہ ایک بے ترتیب پاس ورڈ کے لیے `--generate` شامل کریں۔

</div>

<div dir="rtl">

## مزید جانیں

- [صارف گائیڈ (انگریزی میں)](docs/manual.md) — باقی سب کچھ، قدم بہ قدم
- [تبدیلیوں کی فہرست](CHANGELOG.md)

</div>

<div dir="rtl">

## لائسنس

Apache License 2.0، دیکھیں [LICENSE](LICENSE)۔

</div>
