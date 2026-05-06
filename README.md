# videosrv

A simple HTTP file server written in Go that serves files from a local directory and logs download progress with speed metrics.

## Build

```bash
make build
```

Or install directly to `/usr/local/bin`:

```bash
make install
```

## Usage

```
videosrv <port> <path>
```

- `port` — TCP port to listen on
- `path` — directory to serve files from

**Example:**

```bash
videosrv 8080 /srv/videos
```

The server will be available at `http://0.0.0.0:8080` and will serve all files under `/srv/videos`.

## Downloading a file with curl

Once the server is running, download a file using curl:

```bash
curl -O http://<host>:<port>/<filename>
```

**Example:**

```bash
curl -O http://192.168.1.10:8080/movie.mp4
```

## Update

Pull the latest version and reinstall:

```bash
./update.sh
```
