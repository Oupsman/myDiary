# myDiary

A private diary for the Linux desktop (Go + GTK 4). Each diary is a single
file encrypted with Argon2id + XChaCha20-Poly1305. Entries support fonts,
sizes, bold/italic/underline and embedded pictures, and can carry a mood
(Ctrl+Shift+M) shown as an emoji in the entry list, and tags (Ctrl+Shift+T).
An entry's date can be changed (Ctrl+Shift+D), and "On this day" (Ctrl+D)
lists what you wrote on today's date in earlier years. Search matches titles,
text, moods and tags; start with `#` to search tags only. Every action has a
keyboard shortcut (press F1 in the app).

## Day One import and export

**Import from Day One** (main menu) adds the entries of a Day One export to
the open diary. In Day One, export the journal as **JSON** and pick the zip
(or a lone `.json`). Titles, dates, tags, bold, italic, headings, lists and
photos are kept; photos are shrunk to 2048 pixels and stripped of their
metadata (GPS position included). Videos, audio recordings and PDFs are left
out and replaced by a note in the text. HEIC photos are kept as they are and
show only if the system can read HEIC (on Arch: `libheif`). Importing the same
export twice adds nothing.

**Export** (Ctrl+Shift+E) writes an **unencrypted** zip: one Markdown file
per entry with its title, date, mood and tags as YAML front matter, the
pictures as files, and `diary.json` with everything, formatting included.

## Build

```sh
go build -o mydiary .                                     # the app (needs GTK 4 dev files)
CGO_ENABLED=0 go build -o mydiary-server ./cmd/mydiary-server   # sync server, static binary
```

To build the app and add it to the desktop menu:

```sh
./install.sh                  # for the current user, in ~/.local
./install.sh --system         # for all users, in /usr/local (asks for sudo)
./install.sh --uninstall      # add --system to remove a system install
```

## Sync server

`mydiary-server` keeps diaries in sync between computers. Diaries are uploaded
**already encrypted**; the server never sees a passphrase, a key or any
content, not even file names: diaries are identified by a random ID. It stores
only opaque blobs and a version number. Merging is done by the app, entry by entry: the most recent
version of each entry wins, and deletions propagate.

It speaks plain HTTP on localhost and is meant to sit behind a TLS reverse
proxy reachable only over your private network (e.g. WireGuard).

### Install

```sh
sudo useradd --system --home-dir /var/lib/mydiary --shell /usr/bin/nologin mydiary
sudo install -m 0755 mydiary-server /usr/local/bin/
sudo install -m 0644 deploy/mydiary-server.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now mydiary-server
```

### Devices

Each computer gets its own token, which you can revoke without affecting the
others (no restart needed):

```sh
sudo -u mydiary mydiary-server -data /var/lib/mydiary token add laptop
sudo -u mydiary mydiary-server -data /var/lib/mydiary token list
sudo -u mydiary mydiary-server -data /var/lib/mydiary token revoke laptop
```

In the app, open **Sync settings** (Ctrl+,), enter the proxy URL and the
token. On another computer, use **Get a diary from the sync server**
(Ctrl+Shift+O) and unlock it with its passphrase.

### Reverse proxy

Only expose it on the WireGuard interface. Raise the body size limit: whole
diaries, pictures included, are uploaded in one request.

nginx:

```nginx
server {
    listen 10.0.0.1:443 ssl;          # WireGuard address only
    server_name diary.example.lan;
    ssl_certificate     /etc/ssl/diary.crt;
    ssl_certificate_key /etc/ssl/diary.key;

    client_max_body_size 512m;

    location / {
        proxy_pass http://127.0.0.1:8484;
        proxy_request_buffering off;
    }
}
```

Caddy:

```caddy
diary.example.lan {
    bind 10.0.0.1
    tls internal
    request_body {
        max_size 512MB
    }
    reverse_proxy 127.0.0.1:8484
}
```

If the proxy uses a private CA that is not trusted system-wide, give its PEM
file in the app's sync settings.

### When does it sync?

When a diary is opened, after every save, every two minutes while the diary
has no unsaved changes, and on F5. Unsaved edits are never uploaded. When
offline, changes are uploaded at the next successful sync.

## Web frontend (phone)

`mydiary-server serve -web DIR` also serves a web version of the app, for
reading and writing entries from a phone's browser: entry list, search,
"On this day", moods, tags, dates, text with bold, italic and underline, and
photos from the camera or the library (shrunk on the phone to 2048 pixels,
metadata removed, before upload). Fonts and sizes written on the desktop are
shown and kept, but cannot be set from the web.

The diary is decrypted in the browser by the same Go code as the desktop app,
compiled to WebAssembly (`cmd/mydiary-wasm`): the server still only stores
encrypted files. Build it with `./web/build.sh`, then serve the `web`
directory. The Docker image includes it.

On first use, enter a device token (`token add phone`), then the passphrase.
The token is remembered by the browser, the passphrase never is. Edits are
synced two seconds after you stop typing; the status at the top says when
everything is on the server. Changes not yet synced live only in the open
page: lock (or wait for "Synced") before closing it.

Caveats:

- The server sends the code that receives your passphrase. Whoever controls
  the server or the proxy can change that code to steal the passphrase the
  next time you unlock. Keep it on WireGuard only.
- The whole diary is downloaded at unlock and uploaded at every sync, so
  diaries with many pictures are slow on a phone.
- Use it over HTTPS only: over plain HTTP, anyone on the path can read the
  device token and replace the code. If the proxy uses a private CA, install
  and trust it on the phone (on iOS: Settings → General → About → Certificate
  Trust Settings).

## Docker image

The `Dockerfile` builds the sync server only (static binary on distroless,
non-root, data in `/data`). `.woodpecker.yaml` runs the tests on every push to
`main`, then pushes the image to your registry tagged with the commit and
`latest`.

Woodpecker secrets:

| Secret              | Value                                                       |
|---------------------|-------------------------------------------------------------|
| `registry`          | registry host, e.g. `registry.example.lan`                  |
| `registry_repo`     | full image name, e.g. `registry.example.lan/mydiary-server` |
| `registry_username` | account allowed to push                                     |
| `registry_password` | its password or token                                       |

To run it, create the data directory once (the container runs as uid 65532),
then start the container, publishing the port on an address the reverse proxy
can reach. That link is plain HTTP and carries the device tokens: keep it on
WireGuard. Docker bypasses host firewall rules for published ports.

```sh
sudo install -d -m 0700 -o 65532 -g 65532 /srv/mydiary
docker run -d --name mydiary-server --restart unless-stopped \
    -p 10.0.0.2:8484:8484 -v /srv/mydiary:/data \
    --read-only --cap-drop ALL --security-opt no-new-privileges:true \
    registry.example.lan/mydiary-server:latest
```

To upgrade, `docker pull` the new tag, `docker rm -f mydiary-server` and run
the same command again; the data stays in `/srv/mydiary`.

Tokens:

```sh
docker exec mydiary-server mydiary-server -data /data token add laptop
docker exec mydiary-server mydiary-server -data /data token list
```
