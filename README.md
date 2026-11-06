# Zeno

[Русская версия](README.ru.md)

Voice, video, screen sharing and chat for two people, straight between their computers (WebRTC, peer-to-peer). No server of its own. Windows, closed beta.

## Features

- **Friends connect by themselves.** Exchange friend codes once. After that both apps find each other on their own through an encrypted pair channel on public servers (MQTT and ntfy). Backup ways: a short 6-character code or a long code sent through any messenger.
- **Voice calls.** Opus up to 128 kbit/s, echo cancellation, auto gain, noise gate with an automatic threshold, system-wide push-to-talk, device selection.
- **Noise suppression of your choice.** RNNoise (light), DeepFilterNet3, DPDFNet (cleanest and heaviest), NVIDIA Broadcast (RTX GPU, installed separately), the Chromium built-in one, or off. "Voice only" mode keeps keyboard, clicks and chewing between phrases out of the call.
- **Camera and screen sharing.** Up to 1440p 60 fps with system audio, "smooth" and "sharp" modes, hardware H.264, live stats (frames, bitrate, encoder, ping).
- **Chat.** History (each side keeps its own encrypted copy), typing indicator, delivery marks, files and images.
- **Volume and view.** Friend and stream volume 0-300%, fullscreen stream, frame capture, resizable call area.
- **Built-in apps** (the "Apps" button in chat):
  - **virtualZeno** - a LAN with your friend, like Hamachi: 10.77.x.y addresses, ping, LAN games, shared folders. Works only while both of you run it.
  - **YaGuessr** - guess the place from a Google or Yandex panorama: classic, duel, tug-of-war, bets and other modes. The room lives in the host's Zeno, the friend plays over the data channel.
  - **TarBitTorrent** - send big files and folders, even 50 GB: every piece is verified, transfer resumes after a break or a restart. Only the friend sees the share; it goes directly or through Zeno when a direct link is not possible.
- **Desktop app.** Own window, tray icon, autostart, installs itself with shortcuts, updates itself from GitHub releases.

## How it works

- There is no Zeno server. Media, messages and files go directly between the two PCs. Public STUN servers help find the address; you can add your own TURN server.
- Each user has an Ed25519 key pair. A friend code carries the public key and the beta certificate.
- Connection offers are signed with the sender's key. The offer contains the DTLS fingerprint, so nobody in the middle can pose as your friend.
- The first connection opens only a data channel. Microphone, camera, screen and screen audio are added later by renegotiation over that channel.
- virtualZeno uses the Wintun network adapter (the WireGuard driver). It needs admin rights once: Zeno asks for UAC and installs a small service that holds the adapter. Each address is derived from the public key, and packets with someone else's source address are dropped. Everything is removed in Settings -> Connection.

## Tech stack

- **Go** - the app itself, signaling (MQTT via paho, ntfy over HTTPS), updates, tray.
- **WebView2** - the window; the UI is HTML/JS, calls and chat use Chromium WebRTC.
- **pion/webrtc** - extra P2P links in Go for virtualZeno and file transfer.
- **anacrolix/torrent** - the BitTorrent engine behind TarBitTorrent.
- **AudioWorklet + WebAssembly** - noise suppression; DPDFNet runs in onnxruntime-web.
- **Wintun** - the virtual network adapter for virtualZeno.

## Requirements

- Windows 10 or 11, x64.
- Microsoft Edge WebView2 Runtime (built into Windows 11, usually present on Windows 10).
- A beta login and password from the author.

## Building

Requires Go (version in `go.mod`).

```
build.cmd        # dist\Zeno.exe
go test ./...
```

## Third-party components

- [RNNoise](https://github.com/xiph/rnnoise) 0.2, build [@shiguredo/rnnoise-wasm](https://github.com/shiguredo/rnnoise-wasm) - Apache-2.0.
- [DeepFilterNet3](https://github.com/Rikorose/DeepFilterNet), build [deepfilternet3-noise-filter](https://github.com/mezonai/mezon-noise-suppression) - Apache-2.0 or MIT.
- [DPDFNet](https://github.com/ceva-ip/DPDFNet)2 48 kHz model by Ceva, export from [sherpa-onnx](https://github.com/k2-fsa/sherpa-onnx) - Apache-2.0.
- [onnxruntime-web](https://github.com/microsoft/onnxruntime) 1.30.0 - MIT.
- [Wintun](https://www.wintun.net) 0.14.1, signed by WireGuard LLC.

License texts are in `third_party/`.
