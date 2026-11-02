package users_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/jpeg"
	"image/png"
	"math/rand"
	"testing"

	"github.com/google/uuid"

	"klyvi-api/internal/users"
)

// makePNG renders a w×h gradient PNG so crops are distinguishable from
// stretches if a bug ever swaps them.
func makePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := img.PixOffset(x, y)
			img.Pix[i] = uint8(x % 256)
			img.Pix[i+1] = uint8(y % 256)
			img.Pix[i+3] = 255
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func makeJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

// makeNoisyJPEG renders w×h of random pixels, the worst case for JPEG
// size, so the quality rounds have something to work against.
func makeNoisyJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	rng := rand.New(rand.NewSource(1))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	rng.Read(img.Pix)
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 255
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

// pngHeaderClaiming is a PNG signature and IHDR chunk with the given
// dimensions and no pixel data: a few dozen bytes that would make a naive
// decoder allocate w×h×4 bytes.
func pngHeaderClaiming(w, h uint32) []byte {
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8], ihdr[9] = 8, 6 // 8-bit RGBA

	var buf bytes.Buffer
	buf.WriteString("\x89PNG\r\n\x1a\n")
	_ = binary.Write(&buf, binary.BigEndian, uint32(len(ihdr)))
	chunk := append([]byte("IHDR"), ihdr...)
	buf.Write(chunk)
	_ = binary.Write(&buf, binary.BigEndian, crc32.ChecksumIEEE(chunk))
	return buf.Bytes()
}

// The avatar pipeline is the server-side half of the upload contract: whatever comes in, what goes out is a 512×512 JPEG with no
// metadata (re-encoding drops EXIF by construction).
func TestProcessAvatar(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
	}{
		{"landscape png crops to square", makePNG(t, 800, 400)},
		{"portrait png crops to square", makePNG(t, 300, 900)},
		{"square jpeg stays square", makeJPEG(t, 640, 640)},
		{"tiny image upscales", makePNG(t, 64, 64)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := users.ProcessAvatar(tt.input)
			if err != nil {
				t.Fatalf("ProcessAvatar: %v", err)
			}

			cfg, format, err := image.DecodeConfig(bytes.NewReader(out))
			if err != nil {
				t.Fatalf("decode output: %v", err)
			}
			if format != "jpeg" {
				t.Errorf("output format = %s, want jpeg", format)
			}
			if cfg.Width != 512 || cfg.Height != 512 {
				t.Errorf("output = %dx%d, want 512x512", cfg.Width, cfg.Height)
			}
		})
	}
}

// fakeStorage records the upload so the service test can assert what would
// have hit Supabase without a network.
type fakeStorage struct {
	uploadedPath string
	uploadedType string
	uploadedLen  int
	deletedPath  string
}

func (f *fakeStorage) UploadPublicObject(_ context.Context, _, path, contentType string, data []byte) (string, error) {
	f.uploadedPath, f.uploadedType, f.uploadedLen = path, contentType, len(data)
	return "https://cdn.example/" + path, nil
}

func (f *fakeStorage) DeleteObject(_ context.Context, _, path string) error {
	f.deletedPath = path
	return nil
}

func TestUpdateAvatar(t *testing.T) {
	userID := uuid.New()
	repo := &fakeUserRepo{user: &users.User{ID: userID, Username: "jacob"}}

	t.Run("unconfigured storage is an explicit error", func(t *testing.T) {
		svc := users.NewService(repo)
		_, err := svc.UpdateAvatar(context.Background(), userID, makePNG(t, 100, 100))
		if !errors.Is(err, users.ErrStorageNotConfigured) {
			t.Errorf("error = %v, want ErrStorageNotConfigured", err)
		}
	})

	t.Run("upload over 1 MB is rejected before processing", func(t *testing.T) {
		svc := users.NewService(repo).WithAvatarStorage(&fakeStorage{}, "avatars")
		_, err := svc.UpdateAvatar(context.Background(), userID, make([]byte, 1<<20+1))
		if !errors.Is(err, users.ErrImageTooLarge) {
			t.Errorf("error = %v, want ErrImageTooLarge", err)
		}
	})

	t.Run("non-image is rejected", func(t *testing.T) {
		svc := users.NewService(repo).WithAvatarStorage(&fakeStorage{}, "avatars")
		_, err := svc.UpdateAvatar(context.Background(), userID, []byte("not an image"))
		if !errors.Is(err, users.ErrNotAnImage) {
			t.Errorf("error = %v, want ErrNotAnImage", err)
		}
	})

	t.Run("valid upload stores jpeg under the user id and writes the url", func(t *testing.T) {
		st := &fakeStorage{}
		svc := users.NewService(repo).WithAvatarStorage(st, "avatars")

		user, err := svc.UpdateAvatar(context.Background(), userID, makePNG(t, 800, 400))
		if err != nil {
			t.Fatalf("UpdateAvatar: %v", err)
		}
		if st.uploadedPath != userID.String()+".jpg" {
			t.Errorf("object path = %q, want %q", st.uploadedPath, userID.String()+".jpg")
		}
		if st.uploadedType != "image/jpeg" {
			t.Errorf("content type = %q, want image/jpeg", st.uploadedType)
		}
		if st.uploadedLen == 0 {
			t.Error("nothing was uploaded")
		}
		wantURL := "https://cdn.example/" + userID.String() + ".jpg"
		if repo.lastPatch.AvatarURL == nil || *repo.lastPatch.AvatarURL != wantURL {
			t.Errorf("avatar_url patch = %v, want %s", repo.lastPatch.AvatarURL, wantURL)
		}
		if user == nil {
			t.Fatal("expected updated user row")
		}
	})

	t.Run("delete removes the object and clears the url", func(t *testing.T) {
		st := &fakeStorage{}
		svc := users.NewService(repo).WithAvatarStorage(st, "avatars")

		user, err := svc.DeleteAvatar(context.Background(), userID)
		if err != nil {
			t.Fatalf("DeleteAvatar: %v", err)
		}
		if st.deletedPath != userID.String()+".jpg" {
			t.Errorf("deleted path = %q, want %q", st.deletedPath, userID.String()+".jpg")
		}
		if user == nil || user.AvatarURL != nil {
			t.Errorf("expected cleared avatar_url, got %+v", user)
		}
	})
}

// The source size is read from the header before any pixel is decoded, so a
// file claiming huge dimensions is refused without the allocation.
func TestProcessAvatar_SourceDimensions(t *testing.T) {
	tests := []struct {
		name    string
		input   []byte
		wantErr error
	}{
		{"header claiming 100000x100000 is refused", pngHeaderClaiming(100000, 100000), users.ErrImageTooLarge},
		{"one side over 4096 is refused", pngHeaderClaiming(4097, 16), users.ErrImageTooLarge},
		{"4096 on a side is accepted", makePNG(t, 4096, 64), nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := users.ProcessAvatar(tt.input)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

// Stored avatars stay small even for the worst input: quality steps down in
// rounds until the JPEG fits. Noise at the output size is the hard case,
// since a large source is averaged smooth on the way down.
func TestProcessAvatar_StoredSizeCap(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
	}{
		{"noise at the output size", makeNoisyJPEG(t, 512, 512)},
		{"noise at the largest source size", makeNoisyJPEG(t, 4096, 4096)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := users.ProcessAvatar(tt.input)
			if err != nil {
				t.Fatalf("ProcessAvatar: %v", err)
			}
			if len(out) > 150*1024 {
				t.Errorf("stored avatar = %d bytes, want at most %d", len(out), 150*1024)
			}
		})
	}
}

func TestProcessAvatar_RejectsNonImages(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
	}{
		{"empty", nil},
		{"text", []byte("definitely not an image")},
		{"html", []byte("<html><body>hi</body></html>")},
		{"truncated png", makePNG(t, 100, 100)[:20]},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := users.ProcessAvatar(tt.input)
			if !errors.Is(err, users.ErrNotAnImage) {
				t.Errorf("ProcessAvatar(%s) error = %v, want ErrNotAnImage", tt.name, err)
			}
		})
	}
}
