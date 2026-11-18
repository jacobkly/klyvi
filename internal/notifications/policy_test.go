package notifications_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"klyvi-api/internal/notifications"
)

// policyNotices reads a user's policy notices as "title|link".
func policyNotices(t *testing.T, conn *sqlx.DB, user uuid.UUID) []string {
	t.Helper()
	got := []string{}
	err := conn.SelectContext(context.Background(), &got, `
		select title || '|' || link from notifications
		where user_id = $1 and kind = 'policy' order by id
	`, user)
	if err != nil {
		t.Fatalf("read policy notices: %v", err)
	}
	return got
}

// A policy notice is how the terms keep their promise of notice in the app
// before a material change. It goes to every account once, so running the
// command again by mistake tells nobody twice.
func TestPostPolicy_ReachesEveryAccountOnce(t *testing.T) {
	conn := openDB(t)
	ctx := context.Background()
	svc := notifications.NewService(notifications.NewRepository(conn))
	a, b := newUser(t, conn), newUser(t, conn)

	// Unique per run, and cleaned up, because it reaches every local account.
	text := "Our terms change on 1 March. " + uuid.NewString()
	t.Cleanup(func() {
		_, _ = conn.ExecContext(context.Background(),
			`delete from notifications where kind = 'policy' and title = $1`, text)
	})

	var accounts int64
	if err := conn.GetContext(ctx, &accounts, `select count(*) from users`); err != nil {
		t.Fatalf("count users: %v", err)
	}

	created, err := svc.PostPolicy(ctx, text, "/terms")
	if err != nil {
		t.Fatalf("PostPolicy: %v", err)
	}
	if created != accounts {
		t.Errorf("created %d notices, want one for each of %d accounts", created, accounts)
	}
	for _, user := range []uuid.UUID{a, b} {
		got := policyNotices(t, conn, user)
		if len(got) != 1 || got[0] != text+"|/terms" {
			t.Errorf("notices = %q, want one with the text and link", got)
		}
	}

	again, err := svc.PostPolicy(ctx, text, "/terms")
	if err != nil {
		t.Fatalf("PostPolicy again: %v", err)
	}
	if again != 0 {
		t.Errorf("second run created %d, want 0", again)
	}
	if got := policyNotices(t, conn, a); len(got) != 1 {
		t.Errorf("after a second run, notices = %q, want still one", got)
	}

	// A different change is a new notice, not a duplicate of the first.
	other := text + " Second change."
	t.Cleanup(func() {
		_, _ = conn.ExecContext(context.Background(),
			`delete from notifications where kind = 'policy' and title = $1`, other)
	})
	if _, err := svc.PostPolicy(ctx, other, "/privacy"); err != nil {
		t.Fatalf("PostPolicy other: %v", err)
	}
	if got := policyNotices(t, conn, a); len(got) != 2 {
		t.Errorf("notices = %q, want two after a second change", got)
	}
}

// The list endpoint carries the link and leaves the title fields empty, since
// a policy notice is about no title.
func TestList_APolicyNoticeHasItsLinkAndNoTitle(t *testing.T) {
	conn := openDB(t)
	ctx := context.Background()
	svc := notifications.NewService(notifications.NewRepository(conn))
	user := newUser(t, conn)
	text := "Privacy policy update. " + uuid.NewString()
	t.Cleanup(func() {
		_, _ = conn.ExecContext(context.Background(),
			`delete from notifications where kind = 'policy' and title = $1`, text)
	})
	if _, err := svc.PostPolicy(ctx, text, "/privacy"); err != nil {
		t.Fatalf("PostPolicy: %v", err)
	}

	page, err := svc.List(ctx, user, 10, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Notifications) != 1 {
		t.Fatalf("got %d notices, want 1", len(page.Notifications))
	}
	n := page.Notifications[0]
	if n.Kind != notifications.KindPolicy || n.Title != text {
		t.Errorf("notice = %s %q, want a policy notice with the text", n.Kind, n.Title)
	}
	if n.Link == nil || *n.Link != "/privacy" {
		t.Errorf("link = %v, want /privacy", n.Link)
	}
	if n.MediaType != nil || n.TMDBID != nil || n.SeasonNumber != nil || n.PosterPath != nil {
		t.Errorf("policy notice has title fields: %+v", n)
	}
	if page.UnreadCount != 1 {
		t.Errorf("unread = %d, want 1", page.UnreadCount)
	}
}

// The text and link are checked before anything is written. The link has to be
// a path on the site, so a typo can't send every account somewhere else.
func TestPostPolicy_RefusesBadInput(t *testing.T) {
	svc := notifications.NewService(nil)
	long := make([]byte, notifications.MaxPolicyText+1)
	for i := range long {
		long[i] = 'a'
	}
	cases := []struct {
		name, text, link string
	}{
		{"blank text", "   ", "/terms"},
		{"text too long", string(long), "/terms"},
		{"blank link", "Terms change", ""},
		{"full url", "Terms change", "https://example.com/terms"},
		{"protocol relative", "Terms change", "//example.com"},
		{"not a path", "Terms change", "terms"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.PostPolicy(context.Background(), tc.text, tc.link)
			if !errors.Is(err, notifications.ErrBadPolicy) {
				t.Errorf("err = %v, want ErrBadPolicy", err)
			}
		})
	}
}
