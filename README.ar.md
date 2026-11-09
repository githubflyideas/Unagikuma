# Unagikuma

<p align="center"><img src="docs/images/IMG_8128.jpeg" alt="Unagikuma" width="240"></p>

<div dir="rtl">

**أداة أخرى لمراقبة جاهزية الخدمات: u(ptime) + nagi(os) + kuma(Uptime Kuma).**

**أفضل ما في العالمين:** ملف واحد على جهاز واحد يعمل فورًا، وحين تريد المزيد ضمّ أجهزة أخرى فيصبح عنقودًا. البرنامج نفسه، ولا شيء تنقله.

مراقبة سهلة تستضيفها بنفسك: اعرف فورًا متى يتوقف موقعك أو خادمك، دون إزعاج.

</div>

[English](README.md) · [中文](README.zh.md) · [हिन्दी](README.hi.md) · [Español](README.es.md) · **العربية** · [Français](README.fr.md) · [বাংলা](README.bn.md) · [Português](README.pt.md) · [Русский](README.ru.md) · [Bahasa Indonesia](README.id.md) · [اردو](README.ur.md) · [Deutsch](README.de.md) · [日本語](README.ja.md) · [Tiếng Việt](README.vi.md) · [한국어](README.ko.md)

![قائمة المراقبات بدرجة «ورقي»](docs/images/monitors-paper.png)

<div dir="rtl">

## لماذا Unagikuma؟

- **جاهز خلال 30 ثانية تقريبًا.** نزّل ملفًا واحدًا، شغّله، ثم افتح المتصفح.
- **ملف واحد، ولا شيء آخر لتثبيته.** يعمل على Linux وmacOS وWindows وFreeBSD.
- **يراقب ما يهمك.** المواقع والخوادم والمنافذ وping وDNS وشهادات SSL وتاريخ انتهاء النطاقات.
- **تنبيهات حيث تكون.** البريد الإلكتروني وTelegram وSlack وDiscord وFeishu وDingTalk وWeCom وwebhooks وغيرها.
- **صفحات حالة** تُبقي مستخدميك على اطلاع.
- **انتقال سهل.** انقل مراقباتك من Uptime Kuma أو Nagios أو Checkmate أو Gatus، من صفحة الويب أو من سطر الأوامر.
- **يتحدث لغتك.** الواجهة متاحة بـ 15 لغة.

</div>

<div dir="rtl">

## البدء السريع

**1.** نزّل الملف المناسب لنظامك من [Releases](https://github.com/githubflyideas/Unagikuma/releases/latest) وفُكّ ضغطه.

**2.** شغّله:

</div>

```sh
./unagikuma
```

<div dir="rtl">

على Windows شغّل `unagikuma.exe`.

**3.** افتح http://localhost:8077 في المتصفح وأنشئ حساب المسؤول.

**4.** انقر على **إضافة مراقبة**، وأدخل عنوان موقع، وانتهى الأمر.

> تفتحه من جهاز آخر؟ استخدم `http://<عنوان-الخادم>:8077` وأدخل **رمز الإعداد** الذي عرضه البرنامج عند تشغيله أول مرة.

</div>

<div dir="rtl">

## الانتقال من أداة أخرى

في صفحة الويب افتح **الإعدادات النصية › استيراد**، واختر Uptime Kuma أو Nagios أو Checkmate أو Gatus، وارفع الملف، وراجع المعاينة ثم أضِفه.

</div>

![الاستيراد من Uptime Kuma أو Nagios أو Checkmate أو Gatus في صفحة الويب](docs/images/import.png)

<div dir="rtl">

أو من سطر الأوامر: اطّلع أولًا باستخدام `--dry-run` (لا يتغير شيء)، ثم شغّل الأمر نفسه من دونه.

</div>

```sh
./unagikuma import kuma      --dry-run /path/to/kuma.db
./unagikuma import nagios    --dry-run /etc/nagios4/nagios.cfg
./unagikuma import checkmate --dry-run monitors.json
./unagikuma import gatus     --dry-run config.yaml
```

<div dir="rtl">

Checkmate وGatus: تُنقل فحوص HTTP. ما لا يمكن نقله كما هو يُدرج ويوقف الاستيراد، فلا يُنقل شيء نصف نقل.

</div>

<div dir="rtl">

## نسيت كلمة المرور؟

على الجهاز الذي يعمل عليه Unagikuma:

</div>

```sh
./unagikuma reset-password admin
```

<div dir="rtl">

اكتب كلمة المرور الجديدة مرتين (لن تظهر). أضف `--generate` للحصول على كلمة مرور عشوائية بدلًا من ذلك.

</div>

<div dir="rtl">

## التنبيهات وجهات الاتصال وعنوان الاستماع

- البريد/الرسائل فقط لجهات الاتصال النشطة. اختبار القناة يستخدم نفس القائمة.
- تُعاد محاولة الإشعار الفاشل مرات قليلة (`notify.channel-retries=0` للتعطيل). إعادة محاولة الفحص منفصلة.
- الواجهة افتراضياً HTTP عادي. يُفضّل وكيلاً عكسياً لـ TLS أو `--tls-cert`/`--tls-key`. الوكلاء يستخدمون TLS مثبتاً على نفس المنفذ.

## لمعرفة المزيد

- [دليل المستخدم (بالإنجليزية)](docs/manual.md) — كل ما تبقّى، خطوة بخطوة
- [سجل التغييرات](CHANGELOG.md)

</div>

<div dir="rtl">

## الترخيص

Apache License 2.0، راجع [LICENSE](LICENSE).

</div>
