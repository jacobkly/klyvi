package users

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"

	// Register decoders for the three accepted upload formats. WebP is
	// decode-only in x/image, which is fine — output is always JPEG.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"github.com/google/uuid"
	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const (
	// MaxAvatarBytes is the upload cap. The settings UI shrinks photos to
	// under 400 KB before sending, so this is headroom, not a target.
	MaxAvatarBytes = 1 << 20

	// maxAvatarSourceEdge caps the source's width and height. It is read
	// from the header before any pixel is decoded, so a tiny file claiming
	// huge dimensions can't make the decoder allocate gigabytes.
	maxAvatarSourceEdge = 4096

	// avatarSize is the square edge the stored avatar is normalised to.
	avatarSize = 512

	// maxStoredAvatarBytes is what the stored JPEG aims to fit in. Photos
	// fit at the first quality; noisy images step down.
	maxStoredAvatarBytes = 150 * 1024
)

// avatarJPEGQualities are tried in order until the output fits
// maxStoredAvatarBytes. The last one is kept even if it doesn't fit.
var avatarJPEGQualities = []int{85, 75, 65, 55, 45}

var (
	ErrNotAnImage           = errors.New("not an image")
	ErrImageTooLarge        = errors.New("image too large")
	ErrStorageNotConfigured = errors.New("avatar storage is not configured")
)

// AvatarStorage is the storage surface the users service needs for avatar
// objects. Satisfied by *supabase.Client; declared here per the
// consumer-owns-the-interface convention.
type AvatarStorage interface {
	UploadPublicObject(ctx context.Context, bucket, path, contentType string, data []byte) (string, error)
	DeleteObject(ctx context.Context, bucket, path string) error
}

// avatarObjectPath keys the stored object by user id. One object per user:
// re-uploads overwrite in place, so the URL stays stable and stale avatars
// cannot accumulate.
func avatarObjectPath(userID uuid.UUID) string {
	return userID.String() + ".jpg"
}

// UpdateAvatar processes an uploaded image and stores it, writing the public
// URL onto the user row. Returns the updated row.
func (s *Service) UpdateAvatar(ctx context.Context, userID uuid.UUID, data []byte) (*User, error) {
	if s.storage == nil {
		return nil, ErrStorageNotConfigured
	}
	if len(data) > MaxAvatarBytes {
		return nil, ErrImageTooLarge
	}

	processed, err := ProcessAvatar(data)
	if err != nil {
		return nil, err
	}

	url, err := s.storage.UploadPublicObject(ctx, s.bucket, avatarObjectPath(userID), "image/jpeg", processed)
	if err != nil {
		return nil, fmt.Errorf("store avatar: %w", err)
	}

	return s.repo.UpdateProfile(ctx, userID, UpdateProfilePatch{AvatarURL: &url})
}

// DeleteAvatar removes the stored object and clears avatar_url. Deleting an
// avatar that was never set is a no-op success.
func (s *Service) DeleteAvatar(ctx context.Context, userID uuid.UUID) (*User, error) {
	if s.storage == nil {
		return nil, ErrStorageNotConfigured
	}
	if err := s.storage.DeleteObject(ctx, s.bucket, avatarObjectPath(userID)); err != nil {
		return nil, fmt.Errorf("delete avatar object: %w", err)
	}
	return s.repo.ClearAvatarURL(ctx, userID)
}

// ProcessAvatar normalises an uploaded image into the stored shape: center
// square crop, 512×512, JPEG. Re-encoding is also the metadata strip — EXIF
// (including GPS position, which is the one worth being paranoid about)
// never survives because only pixels are read from the source.
func ProcessAvatar(data []byte) ([]byte, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotAnImage, err)
	}
	if cfg.Width > maxAvatarSourceEdge || cfg.Height > maxAvatarSourceEdge {
		return nil, fmt.Errorf("%w: %dx%d is over %d px on a side",
			ErrImageTooLarge, cfg.Width, cfg.Height, maxAvatarSourceEdge)
	}

	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotAnImage, err)
	}

	// Center square crop.
	b := src.Bounds()
	side := min(b.Dx(), b.Dy())
	x0 := b.Min.X + (b.Dx()-side)/2
	y0 := b.Min.Y + (b.Dy()-side)/2
	crop := image.Rect(x0, y0, x0+side, y0+side)

	// Scale to the target square. CatmullRom is the slowest and best of the
	// x/draw kernels; avatars are small and uploaded rarely, so spend it.
	dst := image.NewRGBA(image.Rect(0, 0, avatarSize, avatarSize))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, crop, xdraw.Over, nil)

	return encodeAvatar(dst)
}

// encodeAvatar steps JPEG quality down until the output fits
// maxStoredAvatarBytes.
func encodeAvatar(img image.Image) ([]byte, error) {
	var out bytes.Buffer
	for _, q := range avatarJPEGQualities {
		out.Reset()
		if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: q}); err != nil {
			return nil, fmt.Errorf("encode avatar: %w", err)
		}
		if out.Len() <= maxStoredAvatarBytes {
			break
		}
	}
	return out.Bytes(), nil
}
