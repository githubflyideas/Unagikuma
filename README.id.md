# Unagikuma

<p align="center"><img src="docs/images/IMG_8128.jpeg" alt="Unagikuma" width="240"></p>

**Satu lagi alat pemantau uptime: u(ptime) + nagi(os) + kuma(Uptime Kuma).**

**Sekali dayung, dua tiga pulau terlampaui:** satu file di satu mesin langsung jalan; saat butuh lebih, gabungkan mesin lain dan jadilah klaster. Program yang sama, tanpa migrasi.

Pemantauan self-hosted yang mudah: langsung tahu saat situs atau server Anda mati, tanpa notifikasi berlebihan.

[English](README.md) · [中文](README.zh.md) · [हिन्दी](README.hi.md) · [Español](README.es.md) · [العربية](README.ar.md) · [Français](README.fr.md) · [বাংলা](README.bn.md) · [Português](README.pt.md) · [Русский](README.ru.md) · **Bahasa Indonesia** · [اردو](README.ur.md) · [Deutsch](README.de.md) · [日本語](README.ja.md) · [Tiếng Việt](README.vi.md) · [한국어](README.ko.md)

![Daftar monitor dengan nada Kertas](docs/images/monitors-paper.png)

## Mengapa Unagikuma

- **Siap dalam sekitar 30 detik.** Unduh satu file, jalankan, lalu buka browser.
- **Cukup satu file, tidak perlu memasang apa pun lagi.** Berjalan di Linux, macOS, Windows, dan FreeBSD.
- **Memeriksa hal yang penting.** Situs web, server, port, ping, DNS, sertifikat SSL, dan masa berlaku domain.
- **Peringatan di tempat Anda biasa berada.** Email, Telegram, Slack, Discord, Feishu, DingTalk, WeCom, webhook, dan lainnya.
- **Halaman status** agar pengguna Anda selalu tahu kabar terbaru.
- **Mudah pindah.** Bawa monitor Anda dari Uptime Kuma, Nagios, Checkmate, atau Gatus, lewat halaman web atau baris perintah.
- **Berbicara dalam bahasa Anda.** Antarmuka tersedia dalam 15 bahasa.

## Mulai cepat

**1.** Unduh file untuk sistem Anda dari [Releases](https://github.com/githubflyideas/Unagikuma/releases/latest) lalu ekstrak.

**2.** Jalankan:

```sh
./unagikuma
```

Di Windows, jalankan `unagikuma.exe`.

**3.** Buka http://localhost:8077 di browser dan buat akun admin Anda.

**4.** Klik **Tambah monitor**, masukkan alamat situs web, selesai.

> Membukanya dari komputer lain? Buka `http://<alamat-server>:8077` dan masukkan **Token penyiapan** yang ditampilkan program saat pertama kali dijalankan.

## Satu mesin, dua mesin, klaster

- **Satu mesin:** jalankan file, tambahkan monitor, selesai. Tanpa basis data, tanpa agen.
- **Dua mesin:** tempel kode bergabung di **Mode operasi**. Setiap mesin memeriksa dan memberi peringatan sendiri, jadi jika satu mati peringatan tetap jalan.
- **Klaster:** tambahkan situs sebanyak yang Anda mau. Setiap peringatan menampilkan apa yang dilihat tiap situs, sehingga "mati di mana-mana" bisa dibedakan dari "mati dari sini saja".

![Monitor yang gagal beserta hasil dari setiap situs](docs/images/detail-sites.png)

## Tanpa banjir peringatan

- **Jaringan sendiri putus?** Satu pesan tentang jaringan, bukan satu per monitor.
- **Host mati?** Layanan di atasnya tidak dilaporkan satu per satu.
- **Paling banyak tiga peringatan per menit per kanal;** sisanya datang sebagai satu ringkasan. Pemulihan selalu langsung dikirim.

## Pindah dari alat lain

Di halaman web, buka **Konfigurasi › Impor**, pilih Uptime Kuma, Nagios, Checkmate, atau Gatus, unggah file-nya, periksa pratinjau, lalu tambahkan.

![Impor dari Uptime Kuma, Nagios, Checkmate, atau Gatus di halaman web](docs/images/import.png)

Atau lewat baris perintah: lihat dulu dengan `--dry-run` (tidak ada yang diubah), lalu jalankan perintah yang sama tanpa itu.

```sh
./unagikuma import kuma      --dry-run /path/to/kuma.db
./unagikuma import nagios    --dry-run /etc/nagios4/nagios.cfg
./unagikuma import checkmate --dry-run monitors.json
./unagikuma import gatus     --dry-run config.yaml
```

Checkmate dan Gatus: pemeriksaan HTTP dipindahkan. Yang tidak bisa dipindahkan persis akan dicantumkan dan menghentikan impor, jadi tidak ada yang setengah jadi.

## Lupa kata sandi?

Di mesin tempat Unagikuma berjalan:

```sh
./unagikuma reset-password admin
```

Ketik kata sandi baru dua kali (tidak ditampilkan). Tambahkan `--generate` untuk mendapatkan kata sandi acak.


## Peringatan, kontak, dan alamat listen

- Email/SMS hanya ke kontak aktif. Tombol uji saluran memakai daftar yang sama.
- Notifikasi gagal dicoba ulang beberapa kali (`notify.channel-retries=0` menonaktifkan). Coba ulang probe terpisah.
- UI default HTTP polos. Lebih baik reverse proxy TLS atau `--tls-cert`/`--tls-key`. Agen sudah memakai TLS terpasang di port yang sama.

## Pelajari lebih lanjut

- [Panduan pengguna (dalam bahasa Inggris)](docs/manual.md) — semua hal lainnya, langkah demi langkah
- [Catatan perubahan](CHANGELOG.md)

## Lisensi

Apache License 2.0, lihat [LICENSE](LICENSE).
