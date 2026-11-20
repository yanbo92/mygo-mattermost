//go:build darwin

package metal

import (
	"bytes"
	"testing"
	"time"

	"github.com/ebitengine/purego"
	"github.com/egoist/mygo/internal/gpu/gputest"
	"github.com/egoist/mygo/internal/scene"
)

// A low-rate scene must not wait hundreds of frames to retire an image that
// disappeared. Backdating use/sweep times avoids a timing-dependent sleep.
func TestImageExpiresAtLowFrameRateAndReuploads(t *testing.T) {
	r, err := newRenderer()
	if err != nil {
		unavailable(t, err)
	}
	defer r.Release()
	img := scene.NewImageRGBA(4, 4, bytes.Repeat([]byte{30, 100, 180, 255}, 16))
	s := &scene.Scene{Width: 16, Height: 16, Clear: scene.Color{A: 255}, Ops: []scene.Op{{Kind: scene.OpImage, Rect: scene.Rect{W: 16, H: 16}, Src: scene.Rect{W: 4, H: 4}, Image: img}}}
	before := renderRaw(t, r, s, false)
	old := r.images[img.ID()]
	if old == nil || old.lastUsed.IsZero() {
		t.Fatal("visible image use was not timestamped")
	}
	old.lastUsed = time.Now().Add(-3 * time.Second)
	r.swept, r.sweptAt = r.frame, time.Now().Add(-2*time.Second)
	pool(r.sweepIfDue)
	if r.images[img.ID()] != nil || old.tex != 0 {
		t.Fatal("hidden texture survived elapsed expiry without a frame threshold")
	}
	after := renderRaw(t, r, s, false)
	if !bytes.Equal(before, after) || r.images[img.ID()] == old {
		t.Fatal("returning to an expired image did not recreate identical pixels")
	}

	// An image touched by this low-rate frame refreshes its age and reuses the
	// existing upload even if its previous use was many seconds ago.
	visible := r.images[img.ID()]
	visible.lastUsed = time.Now().Add(-3 * time.Second)
	var tex id
	pool(func() { tex = r.imageTexture(img) })
	r.sweptAt = time.Now().Add(-2 * time.Second)
	pool(r.sweepIfDue)
	if tex == 0 || r.images[img.ID()] != visible || visible.tex != tex {
		t.Fatal("current image was expired or uploaded again")
	}
}

func TestImageSweepKeepsFrameBoundAndInterval(t *testing.T) {
	r := &Renderer{images: map[uint64]*texture{}, frame: 241, swept: 241, sweptAt: time.Now()}
	fresh := &texture{lastFrame: r.frame, lastUsed: time.Now()}
	old := &texture{lastFrame: r.frame, lastUsed: time.Now().Add(-3 * time.Second)}
	r.images[1], r.images[2] = fresh, old
	r.sweepIfDue()
	if len(r.images) != 2 {
		t.Fatal("elapsed sweeps ran before their interval")
	}
	r.sweptAt = time.Now().Add(-2 * time.Second)
	r.sweepIfDue()
	if r.images[1] != fresh || r.images[2] != nil {
		t.Fatal("elapsed sweep did not distinguish current and expired images")
	}
	// Rapid rendering still preserves the existing frame-count expiry.
	r.images[3] = &texture{lastUsed: time.Now()}
	r.swept = 0
	r.sweepIfDue()
	if r.images[3] != nil || r.images[1] != fresh {
		t.Fatal("frame-count expiry or visible-image retention changed")
	}
}

func TestGPUSourceAndVideoRefreshTextureUse(t *testing.T) {
	r, err := newRenderer()
	if err != nil {
		unavailable(t, err)
	}
	defer r.Release()
	img := scene.NewImageRGBA(4, 4, nil)
	src := &gpuSource{give: true}
	pool(func() {
		src.tex = r.newTexture(4, 4, pixelFormatRGBA8Unorm, usageShaderRead, bytes.Repeat([]byte{0, 100, 0, 255}, 16), 16)
	})
	defer pool(func() { release(&src.tex) })
	img.GPU = src
	pool(func() { r.imageTexture(img) })
	old := r.images[img.ID()]
	if old == nil || old.lastUsed.IsZero() {
		t.Fatal("GPU image use was not timestamped")
	}
	old.lastUsed = time.Now().Add(-3 * time.Second)
	pool(func() { r.imageTexture(img); r.sweep() })
	if r.images[img.ID()] != old {
		t.Fatal("visible GPU source lost its cached texture")
	}

	video := gputest.NV12Frame(16, 8, scene.BT709)
	pool(func() { r.imageTexture(video) })
	v := r.images[video.ID()]
	if v == nil || v.lastUsed.IsZero() {
		t.Fatal("uploaded video use was not timestamped")
	}
	v.lastUsed = time.Now().Add(-3 * time.Second)
	pool(func() { r.imageTexture(video); r.sweep() })
	if r.images[video.ID()] != v {
		t.Fatal("visible video texture expired")
	}
	video.Changed()
	v.lastUsed = time.Now().Add(-3 * time.Second)
	pool(func() { r.imageTexture(video); r.sweep() })
	if r.images[video.ID()] != v || v.ver != video.Version() {
		t.Fatal("changed uploaded video did not refresh cached use")
	}
}

func TestExpiredVideoWrapperLivesUntilInflightSlotCompletes(t *testing.T) {
	r, err := newRenderer()
	if err != nil {
		unavailable(t, err)
	}
	defer r.Release()
	if err := loadCoreVideo(); err != nil {
		t.Fatal(err)
	}
	img := gputest.NV12Frame(16, 8, scene.BT709)
	buffer := pixelBuffer(t, img)
	defer purego.SyscallN(cfRelease, buffer)
	img.Surface.Native = buffer
	pool(func() { r.imageTexture(img) })
	old := r.images[img.ID()]
	if old == nil || old.cv[0] == 0 || old.lastUsed.IsZero() {
		t.Fatal("IOSurface video was not wrapped/timestamped")
	}
	wrappers := old.cv
	// Commit a frame that uses the wrapped planes, retaining the slot until
	// waitSlot observes completion. Expiry must transfer wrappers to that slot.
	s := &scene.Scene{Width: 16, Height: 8, Clear: scene.Color{A: 255}, Ops: []scene.Op{{Kind: scene.OpImage, Rect: scene.Rect{W: 16, H: 8}, Src: scene.Rect{W: 16, H: 8}, Image: img}}}
	var target id
	pool(func() {
		target = r.newTexture(16, 8, pixelFormatBGRA8Unorm, usageRenderTarget|usageShaderRead, nil, 0)
		cb, encodeErr := r.encode(s, target)
		if encodeErr != nil {
			err = encodeErr
			return
		}
		send(cb, "commit")
		r.track(cb)
	})
	defer pool(func() { release(&target) })
	if err != nil {
		t.Fatal(err)
	}
	slot := r.inUse
	old.lastUsed = time.Now().Add(-3 * time.Second)
	pool(r.sweep)
	if r.images[img.ID()] != nil || len(r.video.held[slot]) != 2 || r.video.held[slot][0] != wrappers[0] {
		t.Fatal("expired wrappers were not retained by their inflight frame slot")
	}
	pool(func() {
		if cvMetalTextureGetTexture(r.video.held[slot][0]) == 0 {
			t.Error("wrapper released before frame completion")
		}
		r.waitSlot(slot)
	})
	if len(r.video.held[slot]) != 0 || r.err != nil {
		t.Fatal("completed frame did not release wrappers or failed rendering", r.err)
	}
	pool(func() { r.imageTexture(img) })
	if current := r.images[img.ID()]; current == nil || current == old || current.lastUsed.IsZero() {
		t.Fatal("returning to wrapped video did not recreate the cache entry")
	}
}
