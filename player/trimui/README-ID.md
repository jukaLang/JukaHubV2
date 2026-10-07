# JukaHub di TrimUI Smart Pro (Stock OS)

Paket siap-salin untuk **TrimUI Smart Pro (Allwinner A133P, Linux ARM64, stock OS)**.

Isi paket:

```
Apps/JukaHub/
├── config.json          ← registrasi app di launcher stock
├── icon.png             ← ikon di menu Apps
├── launch.sh            ← dijalankan launcher untuk mulai JukaHub
├── JukaHub              ← binary ARM64 (dibangun di CI)
├── jukaconfig.json      ← konfigurasi tampilan (versi ringkas: YouTube)
├── Inter-Regular.ttf    ← font
├── background.jpg
├── VERSION.txt
└── required/            ← alat bantu, semua versi ARM64
    ├── yt-dlp           ← ambil info/stream YouTube (tanpa Python)
    ├── ffmpeg           ← dekoder untuk playback video
    ├── ffprobe
    └── ffplay
```

Yang TIDAK perlu ada di perangkat: Python, Docker, Go, toolchain apa pun.
Semua library SDL2 (2.26.1 + image + ttf) sudah ada di firmware stock.

---

## 1. Cara build paketnya (GitHub Actions)

Build ARM64 butuh toolchain, jadi dikerjakan di GitHub Actions
(menggunakan image toolchain TrimUI komunitas, sudah berisi SDK A133P + gcc
aarch64 + SDL2 + Go).

1. Commit & push perubahan ke repository.
2. Buka tab **Actions** → workflow **Build SD card package** → **Run workflow**.
   (Workflow juga jalan otomatis setiap push ke `main`.)
3. Tunggu ±3–6 menit. Download artifact **JukaHub-SDCARD**.
4. Di dalam artifact ada `JukaHub-SDCARD.zip`.

Alternatif tanpa GitHub (punya PC Linux / WSL / VPS):

```bash
cd player
SYSROOT=/usr/local/aarch64-linux-gnu-7.5.0-linaro/sysroot ./package-sdcard.sh
```

---

## 2. Pasang ke SD card

1. Matikan perangkat, cabut SD card.
2. Extract `JukaHub-SDCARD.zip`. Foldernya sudah berbentuk `Apps/JukaHub/...`
   — **langsung salin ke root SD card**, timpa/merge dengan folder `Apps` yang
   sudah ada. Hasilnya:

   ```
   SDCARD/
   ├── Apps/
   │   └── JukaHub/       ← folder baru
   ├── RetroArch/
   ├── Roms/
   └── ... (folder stock lain, jangan dihapus)
   ```
3. Masukkan SD card, nyalakan perangkat.
4. Buka menu **Apps** → **JukaHub**.

Kalau `Apps/JukaHub` tidak muncul di menu, matikan perangkat dulu lalu nyalakan
lagi (launcher stock membaca folder `Apps` saat boot).

---

## 3. Kalau ada masalah

| Gejala | tackling |
|---|---|
| Layar hitam / tidak keluar dari app | Buka kartu SD di PC, kirim isi `Apps/JukaHub/errors.txt` ke saya. |
| App muncul tapi langsung keluar | Cek `errors.txt` — biasanya `JukaHub` belum executable (`chmod +x`) atau library kurang. |
| Huruf tidak tampil / kotak-kotak | `Inter-Regular.ttf` hilang dari folder app. |
| Pencarian YouTube gagal | Cek `required/yt-dlp` ada; butuh Wi-Fi. Lihat juga `logs.txt` (scene **Logs** di Beranda). |
| Video lambat / tersendat | Turunkan playback resolution di Settings ke `360` atau `240`. |

Semua log aplikasi disimpan di:

* `Apps/JukaHub/errors.txt` — output process + launcher
* `Apps/JukaHub/logs.txt` — log internal app (tombol **Logs** di Beranda)

Kirim file itu ke sini kalau ada masalah, tanpa perlu SSH.

---

## 4. Catatan teknis (untuk penyetelan lanjutan)

* **Arsitektur:** TrimUI Smart Pro = Allwinner A133P, userspace **aarch64**
  (glibc 2.23), display 1280×768, RAM 1–2 GB, 1 core Cortex-A7.
* **Library:** binary di-link ke SONAME yang ada di firmware stock
  (`libSDL2-2.0.so.0`, `libSDL2_image-2.0.so.0`, `libSDL2_ttf-2.0.so.0`),
  jadi tidak perlu membawa `.so` sendiri.
* **Python tidak ada di stock OS**, jadi `yt-dlp` dipakai dalam bentuk binary
  statis resmi `yt-dlp_linux_aarch64`.
* **ffmpeg** dipakai sebagai build statis ARM64 dari BtbN (ffmpeg/ffprobe/ffplay).
* **Config ringkas:** `player/trimui/make-config.ps1` membuat
  `player/trimui/jukaconfig.device.json` dari config penuh — hanya menyisakan
  Main / Tube / Shorts / Favorites / Settings / Logs / Exit.
  Namanya sengaja **bukan** `jukaconfig.json` karena `player/.gitignore`
  mengecualikan file itu (bisa berisi API key user) — kalau nama sama, config
  perangkat tidak ikut ter-commit dan build CI akan gagal.
  Jalankan ulang script itu kalau config penuh berubah:

  ```powershell
  cd player/trimui
  powershell -File .\make-config.ps1 -Src ..\jukaconfig.json -Dst .\jukaconfig.device.json
  ```
* **Perubahan kode untuk perangkat:**
  * `videoFormatSelector()` — batas resolusi playback ikut `playbackResolution`.
  * `SDL_AUDIODRIVER=directsound` sekarang hanya di Windows (sebelumnya
    membuat SDL gagal buka audio di Linux).