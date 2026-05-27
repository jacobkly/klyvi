package users

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// User mirrors the `users` table. The `id` column is the Supabase auth UID;
// the row is created on first authenticated request via EnsureUser.
type User struct {
	ID             uuid.UUID        `db:"id" json:"id"`
	Username       string           `db:"username" json:"username"`
	Bio            *string          `db:"bio" json:"bio"`
	AvatarURL      *string          `db:"avatar_url" json:"avatar_url"`
	BannerURL      *string          `db:"banner_url" json:"banner_url"`
	FavoriteMedia  *json.RawMessage `db:"favorite_media" json:"favorite_media"`
	FavoritePeople *json.RawMessage `db:"favorite_people" json:"favorite_people"`
	IsActive       bool             `db:"is_active" json:"is_active"`
	CreatedAt      time.Time        `db:"created_at" json:"created_at"`
	UpdatedAt      time.Time        `db:"updated_at" json:"updated_at"`
}
