package users

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// User mirrors the `users` table. The `id` column is the Supabase auth UID;
// the row is created on first authenticated request via EnsureUser.
type User struct {
	ID       uuid.UUID `db:"id" json:"id"`
	Username string    `db:"username" json:"username"`
	// UsernameChangedAt is nil when the user has never renamed away from the
	// auto-generated name, which is what makes the first change free. The
	// client reads it to show when the next change unlocks.
	UsernameChangedAt *time.Time       `db:"username_changed_at" json:"username_changed_at"`
	Birthday          *DateOnly        `db:"birthday" json:"birthday"`
	Bio               *string          `db:"bio" json:"bio"`
	AvatarURL         *string          `db:"avatar_url" json:"avatar_url"`
	BannerURL         *string          `db:"banner_url" json:"banner_url"`
	FavoriteMedia     *json.RawMessage `db:"favorite_media" json:"favorite_media"`
	FavoritePeople    *json.RawMessage `db:"favorite_people" json:"favorite_people"`
	// Settings is an opaque client-owned JSON object (theme, notification
	// toggles, list preferences, …). The server validates only shape and
	// size; the client owns the schema and the merge.
	Settings json.RawMessage `db:"settings" json:"settings"`
	IsActive          bool             `db:"is_active" json:"is_active"`
	CreatedAt         time.Time        `db:"created_at" json:"created_at"`
	UpdatedAt         time.Time        `db:"updated_at" json:"updated_at"`
}
