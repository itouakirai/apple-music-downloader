# Apple Music ALAC / Dolby Atmos Downloader

[English](./README.md) | [简体中文](./README-CN.md) | [🌐 Cloud Server / Proxy Setup](./PROXY-SETUP.md)

> **Original script by Sorrow.** Modified with fixes and improvements.

---

## ⚠️ Prerequisites

**Must be installed first:**

- **[MP4Box](https://gpac.io/downloads/gpac-nightly-builds/)** - Ensure it's correctly added to your environment variables
 - **[wrapper-lite](https://github.com/WorldObservationLog/wrapper/tree/lite)** - Decryption program must be running before use. Point `lite-server` in `config.yaml` to its HTTP API (e.g. `http://127.0.0.1:12340`)

**Optional (for MV download):**

- **[mp4decrypt](https://www.bento4.com/downloads/)**

---

## ✨ Features

1. **Inline Covers & LRC Lyrics**
2. **Word-by-word & Out-of-sync Lyrics** support
3. **Artist Album Download** - Automatically download all albums from an artist
   ```bash
   go run main.go https://music.apple.com/us/artist/taylor-swift/159260351 --all-album
   ```
4. **Stream Decryption** - Uses Sendy McSenderson's code for download-and-decrypt streaming, solving memory issues with large files
5. **MV Download** - Requires mp4decrypt installation
6. **Interactive Search** - Arrow-key navigation for search results
   ```bash
   go run main.go --search [song/album/artist] "search_term"
   ```

---

## 🎵 Supported Audio Formats

| Format | Description | Requires Subscription |
|--------|-------------|----------------------|
| `alac` | audio-alac-stereo | ✅ |
| `ec3` | audio-atmos / audio-ec3 | ✅ |
| `aac` | audio-stereo | ✅ |
| `aac-lc` | audio-stereo | ✅ |
| `aac-binaural` | audio-stereo-binaural | ✅ |
| `aac-downmix` | audio-stereo-downmix | ✅ |
| `MV` | Music Video | ✅ |

> **Note:** For `station`, you must provide a valid `media-user-token` from an active subscription.

---

## 🚀 Usage

### Running with Docker

1. Ensure the [wrapper-lite](https://github.com/WorldObservationLog/wrapper/tree/lite) decryption program is running and `lite-server` is set in `config.yaml`

2. Start the downloader:

```bash
# Show help
docker run --network host -v ./downloads:/downloads ghcr.io/zhaarey/apple-music-downloader --help

# Download albums
docker run --network host -v ./downloads:/downloads ghcr.io/zhaarey/apple-music-downloader https://music.apple.com/ru/album/children-of-forever/1443732441

# Download single song
docker run --network host -v ./downloads:/downloads ghcr.io/zhaarey/apple-music-downloader https://music.apple.com/ru/album/bass-folk-song/1443732441?i=1443732453

# Interactive selection
docker run -it --network host -v ./downloads:/downloads ghcr.io/zhaarey/apple-music-downloader --select https://music.apple.com/ru/album/children-of-forever/1443732441

# Download playlists
docker run --network host -v ./downloads:/downloads ghcr.io/zhaarey/apple-music-downloader https://music.apple.com/us/playlist/taylor-swift-essentials/pl.3950454ced8c45a3b0cc693c2a7db97b

# Dolby Atmos
docker run --network host -v ./downloads:/downloads ghcr.io/zhaarey/apple-music-downloader --atmos https://music.apple.com/us/album/1989-taylors-version-deluxe/1713845538

# AAC format
docker run --network host -v ./downloads:/downloads ghcr.io/zhaarey/apple-music-downloader --aac https://music.apple.com/us/album/1989-taylors-version-deluxe/1713845538

# Debug/View quality
docker run --network host -v ./downloads:/downloads ghcr.io/zhaarey/apple-music-downloader --debug https://music.apple.com/ru/album/miles-smiles/209407331
```

**Custom Configuration:**

Mount your own `config.yaml`:

```bash
docker run --network host -v ./downloads:/downloads -v ./config.yaml:/app/config.yaml ghcr.io/zhaarey/apple-music-downloader [args]
```

> **Note:** Ensure `config.yaml` exists in your current directory before running. If it doesn't exist, Docker will create an empty directory instead of a file, causing the container to fail.

---

### Running Locally (Go)

1. Ensure the [wrapper-lite](https://github.com/WorldObservationLog/wrapper/tree/lite) decryption program is running and `lite-server` is set in `config.yaml`

2. **Download albums:**
   ```bash
   go run main.go https://music.apple.com/us/album/whenever-you-need-somebody-2022-remaster/1624945511
   ```

3. **Download single song:**
   ```bash
   go run main.go https://music.apple.com/us/album/never-gonna-give-you-up-2022-remaster/1624945511?i=1624945512
   # or
   go run main.go https://music.apple.com/us/song/you-move-me-2022-remaster/1624945520
   ```

4. **Interactive selection:**
   ```bash
   go run main.go --select https://music.apple.com/us/album/whenever-you-need-somebody-2022-remaster/1624945511
   ```
   Enter track numbers separated by spaces.

5. **Download playlists:**
   ```bash
   go run main.go https://music.apple.com/us/playlist/taylor-swift-essentials/pl.3950454ced8c45a3b0cc693c2a7db97b
   # or
   go run main.go https://music.apple.com/us/playlist/hi-res-lossless-24-bit-192khz/pl.u-MDAWvpjt38370N
   ```

6. **Dolby Atmos:**
   ```bash
   go run main.go --atmos https://music.apple.com/us/album/1989-taylors-version-deluxe/1713845538
   ```

7. **AAC format:**
   ```bash
   go run main.go --aac https://music.apple.com/us/album/1989-taylors-version-deluxe/1713845538
   ```

8. **View quality info:**
   ```bash
   go run main.go --debug https://music.apple.com/us/album/1989-taylors-version-deluxe/1713845538
   ```

📖 [Chinese Tutorial (Method 3)](https://telegra.ph/Apple-Music-Alac%E9%AB%98%E8%A7%A3%E6%9E%90%E5%BA%A6%E6%97%A0%E6%8D%9F%E9%9F%B3%E4%B9%90%E4%B8%8B%E8%BD%BD%E6%95%99%E7%A8%8B-04-02-2)

---

## 📝 Getting media-user-token (For station)

1. Open [Apple Music](https://music.apple.com) and log in
2. Open Developer Tools (F12)
3. Navigate to `Application → Storage → Cookies → https://music.apple.com`
4. Find the cookie named `media-user-token` and copy its value
5. Paste the value into `config.yaml` under the `media-user-token` setting
6. Save the file and start the script

---

## 🌐Translation & Pronunciation Lyrics 
Set lrc-extra in `config.yaml`

---

## 🖥️ Running on a Cloud Server (DigitalOcean / VPS)?

If you're getting `503 Service Unavailable`  on your server, Apple Music's API likely blocks your server's IP.

➡️ **See the full fix guide: [PROXY-SETUP.md](./PROXY-SETUP.md)**

Quick summary — add this to `config.yaml` after setting up [Cloudflare WARP](./PROXY-SETUP.md#2-option-a--cloudflare-warp-recommended) or an [SSH tunnel](./PROXY-SETUP.md#3-option-b--ssh-reverse-tunnel-zero-cost-no-install):
```yaml
proxy: "socks5://127.0.0.1:1080"
```

---

## 👏 Special Thanks

- **WorldObservationLog** - Created [wrapper / wrapper-lite](https://github.com/WorldObservationLog/wrapper) (backend decryption service)

---
