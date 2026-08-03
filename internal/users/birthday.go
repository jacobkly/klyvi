package users

import (
	"bytes"
	"encoding/json"
	"errors"
	"time"
)

var ErrBirthdayInvalid = errors.New("invalid birthday")

// DateOnly renders a Postgres DATE as "YYYY-MM-DD" in JSON instead of the
// midnight-timestamp shape a bare time.Time would produce.
type DateOnly struct {
	time.Time
}

func (d DateOnly) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.Format("2006-01-02"))
}

func (d *DateOnly) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	t, err := time.ParseInLocation("2006-01-02", s, time.UTC)
	if err != nil {
		return err
	}
	d.Time = t
	return nil
}

// Scan implements sql.Scanner so the DATE column loads directly.
func (d *DateOnly) Scan(src any) error {
	switch v := src.(type) {
	case time.Time:
		d.Time = v
		return nil
	case nil:
		return nil
	default:
		return errors.New("unsupported scan type for DateOnly")
	}
}

// birthdayMin is the oldest accepted birthday. Anything earlier is treated
// as a typo, not a user.
var birthdayMin = time.Date(1900, time.January, 1, 0, 0, 0, 0, time.UTC)

// OptionalDate is a tri-state PATCH field: Set=false means the client did
// not mention it (leave unchanged); Set=true with a nil Value means clear;
// Set=true with a Value means write it. The COALESCE fields on the profile
// patch cannot express "clear", which is why this type exists.
type OptionalDate struct {
	Set   bool
	Value *time.Time
}

// DecodeUpdateProfileRequest decodes a PATCH /v1/users/me body, preserving
// the absent-vs-null distinction for tri-state fields. The handler and the
// tests share this so the semantics cannot drift between them.
func DecodeUpdateProfileRequest(body []byte) (UpdateProfileRequest, error) {
	var req UpdateProfileRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return UpdateProfileRequest{}, err
	}
	return req, nil
}

// parseBirthday turns the raw JSON birthday field into the tri-state patch
// value. raw == nil means the key was absent.
func parseBirthday(raw json.RawMessage) (OptionalDate, error) {
	if raw == nil {
		return OptionalDate{}, nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return OptionalDate{Set: true}, nil
	}

	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return OptionalDate{}, ErrBirthdayInvalid
	}
	d, err := time.ParseInLocation("2006-01-02", s, time.UTC)
	if err != nil {
		return OptionalDate{}, ErrBirthdayInvalid
	}
	if d.Before(birthdayMin) || d.After(time.Now().UTC()) {
		return OptionalDate{}, ErrBirthdayInvalid
	}
	return OptionalDate{Set: true, Value: &d}, nil
}
