# ZimZim

<p align="center"><img src="web/icon.svg" width="120" alt="ZimZim"></p>

A tiny self-hosted walkie-talkie for your server. One Go binary, no database, no accounts in the cloud.

- Make a walkie for anyone. Each one gets its own **secret link** plus a **username and password**.
- Each walkie has as many **codes** as you like, each with an on/off switch. Two walkies hear each other when they share a code and both have it on.
- **Hold to talk**, or flip **LIVE** to stay on air.
- Hidden **quick words** (Hii…, Call me…) and a tiny text box. They show up faded on the other side, then fade away.
- Voice goes through your server over a WebSocket, so it works behind any NAT. No TURN/STUN needed.

## Install

```bash
bash <(curl -Ls https://raw.githubusercontent.com/sahand363/zimzim/main/install.sh)
```

The installer asks for the web UI port, shows progress, then opens the menu. Open the menu any time with:

```bash
zimzim
```

The menu shows your admin panel link on top, and lets you change the admin login, get SSL (Let's Encrypt with a domain, or self-signed by IP), change port, make a new secret admin link, restart, view logs, update and uninstall.

> Phones only allow the microphone over **HTTPS**, so get SSL from the menu before using it remotely.

## Use

1. Open the admin panel link from the menu and sign in.
2. Tap **+ New**, give it a name and codes. The link and login are copied for you to send.
3. The other person opens the link, unlocks it, turns the knob, and holds the orange key to talk.
4. On Android/iPhone, use **Add to home screen** to keep it like an app.

## Android app

Grab the APK from the latest **android-v…** release, install it, paste your walkie link and turn the knob. It keeps listening with the screen off (a small "ZimZim is on" notification stays while it's on; tap **Turn off** there to stop). It also accepts your own server's self-signed certificate, so SSL without a domain works in the app.

## Files

- `/usr/local/zimzim/zimzim-server`: the server
- `/etc/zimzim/config.json`: settings and walkies (passwords are salted hashes)
- `zimzim-server help`: CLI used by the menu
