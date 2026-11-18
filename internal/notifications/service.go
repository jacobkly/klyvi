package notifications

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

// ErrBadPolicy is a policy notice whose text or link can't be posted.
var ErrBadPolicy = errors.New("bad policy notice")

// Service is what the API and the account export use.
type Service struct {
	repo *Repository
}

// NewService builds a Service on a Repository.
func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
}

// List returns one page of the user's notices and their unread count. It reads
// one row past the page to learn whether there is more.
func (s *Service) List(ctx context.Context, userID uuid.UUID, limit, offset int) (*Page, error) {
	if limit < 1 {
		limit = DefaultPageSize
	}
	if limit > MaxPageSize {
		limit = MaxPageSize
	}
	if offset < 0 {
		offset = 0
	}

	notices, err := s.repo.List(ctx, userID, limit+1, offset)
	if err != nil {
		return nil, err
	}
	unread, err := s.repo.CountUnread(ctx, userID)
	if err != nil {
		return nil, err
	}

	hasMore := len(notices) > limit
	if hasMore {
		notices = notices[:limit]
	}
	return &Page{Notifications: notices, UnreadCount: unread, HasMore: hasMore}, nil
}

// ListAll returns every notice the user has.
func (s *Service) ListAll(ctx context.Context, userID uuid.UUID) ([]Notification, error) {
	return s.repo.ListAll(ctx, userID)
}

// MarkRead marks the given notices read and returns the unread count left.
func (s *Service) MarkRead(ctx context.Context, userID uuid.UUID, ids []int64) (int, error) {
	if err := s.repo.MarkRead(ctx, userID, ids); err != nil {
		return 0, err
	}
	return s.repo.CountUnread(ctx, userID)
}

// MarkAllRead marks everything read and returns the unread count left.
func (s *Service) MarkAllRead(ctx context.Context, userID uuid.UUID) (int, error) {
	if err := s.repo.MarkAllRead(ctx, userID); err != nil {
		return 0, err
	}
	return s.repo.CountUnread(ctx, userID)
}

// PostPolicy posts a policy notice to every account and says how many it
// created. The text is trimmed and capped at MaxPolicyText characters. The link
// must be a path on the site, such as /terms, so a typo can't send every
// account to another site.
func (s *Service) PostPolicy(ctx context.Context, text, link string) (int64, error) {
	text = strings.TrimSpace(text)
	if text == "" || utf8.RuneCountInString(text) > MaxPolicyText {
		return 0, fmt.Errorf("%w: text must be 1 to %d characters", ErrBadPolicy, MaxPolicyText)
	}
	if !strings.HasPrefix(link, "/") || strings.HasPrefix(link, "//") || strings.ContainsAny(link, " \\") {
		return 0, fmt.Errorf("%w: link must be a path on the site, such as /terms", ErrBadPolicy)
	}
	return s.repo.InsertPolicy(ctx, text, link)
}
