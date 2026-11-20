# Mattermost downstream patch

This repository snapshots the official github.com/egoist/mygo v0.9.2 Go module
source. Its original module path and MIT license are retained. The original
module checksum is h1:4Yscy6Xrr8iusJyC2rYgQWUhuDjyLRSIvgNTOv2Zc0c=.
The snapshot's first commit is unmodified upstream module content; the next
commit changes only Metal hidden-image texture expiry and its tests.

Mattermost uses an immutable remote Go module replacement; it does not load
a developer-local module path. No APIs, caret behavior, drawable count, shader
selection, glyph atlas or other platforms are changed. Hidden image textures
can be swept once per second and expire after two seconds of non-use, retaining
the existing frame-count bound. Visible image/GPU/video uses refresh their age.
Inflight CV wrappers follow the existing frame-slot lifetime.

Run with Go 1.27.4 and CGO_ENABLED=0:

```sh
MYGO_GPU_REQUIRED=1 go test ./internal/gpu/metal
go test ./...
go vet ./...
go build ./...
GOOS=windows go build ./...
GOOS=linux go build ./...
```

The targeted regressions check low-frame-rate expiry, restored image pixels,
visible texture reuse, time/frame bounds, GPU/video use and CV wrappers retained
until their inflight slot is observed complete. The tests fail with the old
expiry policy and pass with the patch. Application measurements and integration
records live in yanbo92/mattermost-desktop docs/memory.md and issue #25.

Remove the replacement after the official dependency includes an equivalent
fix and passes those regressions. Do not accumulate unrelated changes here.

## Initial verification environment

macOS 27.2 arm64, Go 1.27.4. macOS/Linux/Windows all-package builds and host
vet pass. Full Metal tests (including the new expiry tests with GPU required)
pass. Counter-native release size changes 10,037,650 -> 10,037,666 bytes (+16);
hello is unchanged at 9,815,778 bytes.

The broad upstream go test ./... attempt has known environment failures in
cmd/mygo and internal/nativegen (Homebrew cargo cannot load llhttp 9.3, and
Swift build output is not the test's expected English) and permissionflow
(locale/OS permission titles). These do not arise in the changed Metal package.
The same selected failures are checked against official 0.9.2; do not present
this environment as a full-framework green suite. App-level verification and
packaged native acceptance remain separate requirements.
