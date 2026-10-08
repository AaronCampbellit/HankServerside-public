package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

func TestCreateOrCoalesceUserNotificationIsIdempotentAndTimeBounded(t *testing.T) {
	t.Parallel()
	ctx, db, user, home, now := notificationStoreFixture(t, "coalesce")
	defer db.Close()

	first, created, err := db.CreateOrCoalesceUserNotification(ctx, notificationInput("ntf_first", user.ID, home.ID, "event-one", "agent:one", "offline", now))
	if err != nil || !created {
		t.Fatalf("create first = %#v, %v, created=%v", first, err, created)
	}
	coalesced, created, err := db.CreateOrCoalesceUserNotification(ctx, notificationInput("ntf_second", user.ID, home.ID, "event-two", "agent:one", "offline", now.Add(2*time.Minute)))
	if err != nil || created || coalesced.ID != first.ID || coalesced.OccurrenceCount != 2 {
		t.Fatalf("coalesced = %#v, %v, created=%v", coalesced, err, created)
	}
	replayed, created, err := db.CreateOrCoalesceUserNotification(ctx, notificationInput("ntf_replay", user.ID, home.ID, "event-two", "agent:one", "offline", now.Add(3*time.Minute)))
	if err != nil || created || replayed.ID != first.ID || replayed.OccurrenceCount != 2 {
		t.Fatalf("replayed = %#v, %v, created=%v", replayed, err, created)
	}

	later, created, err := db.CreateOrCoalesceUserNotification(ctx, notificationInput("ntf_later", user.ID, home.ID, "event-three", "agent:one", "offline", now.Add(10*time.Minute)))
	if err != nil || !created || later.ID == first.ID {
		t.Fatalf("later = %#v, %v, created=%v", later, err, created)
	}
}

func TestCreateOrCoalesceUserNotificationDoesNotMergeDelayedEventIntoFutureOccurrence(t *testing.T) {
	t.Parallel()
	ctx, db, user, home, now := notificationStoreFixture(t, "delayed")
	defer db.Close()

	future, created, err := db.CreateOrCoalesceUserNotification(ctx, notificationInput("ntf_future", user.ID, home.ID, "event-future", "agent:delayed", "offline", now.Add(10*time.Minute)))
	if err != nil || !created {
		t.Fatalf("create future = %#v, %v, created=%v", future, err, created)
	}
	delayed, created, err := db.CreateOrCoalesceUserNotification(ctx, notificationInput("ntf_delayed", user.ID, home.ID, "event-delayed", "agent:delayed", "offline", now))
	if err != nil || !created || delayed.ID == future.ID {
		t.Fatalf("delayed = %#v, %v, created=%v", delayed, err, created)
	}
}

func TestReadUserNotificationStopsCoalescing(t *testing.T) {
	t.Parallel()
	ctx, db, user, home, now := notificationStoreFixture(t, "read")
	defer db.Close()

	first, _, err := db.CreateOrCoalesceUserNotification(ctx, notificationInput("ntf_read_first", user.ID, home.ID, "event-read-one", "agent:read", "offline", now))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MarkUserNotificationRead(ctx, user.ID, first.ID, now.Add(time.Minute)); err != nil {
		t.Fatalf("MarkUserNotificationRead: %v", err)
	}
	second, created, err := db.CreateOrCoalesceUserNotification(ctx, notificationInput("ntf_read_second", user.ID, home.ID, "event-read-two", "agent:read", "offline", now.Add(2*time.Minute)))
	if err != nil || !created || second.ID == first.ID {
		t.Fatalf("second after read = %#v, %v, created=%v", second, err, created)
	}
}

func TestListUserNotificationsPaginatesAndCountsUnread(t *testing.T) {
	t.Parallel()
	ctx, db, user, home, now := notificationStoreFixture(t, "page")
	defer db.Close()

	for index, id := range []string{"ntf_page_one", "ntf_page_two", "ntf_page_three"} {
		input := notificationInput(id, user.ID, home.ID, "event-"+id, "collapse-"+id, "changed", now.Add(time.Duration(index)*time.Minute))
		if _, _, err := db.CreateOrCoalesceUserNotification(ctx, input); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	page, err := db.ListUserNotifications(ctx, user.ID, domain.NotificationListOptions{Limit: 2})
	if err != nil {
		t.Fatalf("ListUserNotifications: %v", err)
	}
	if len(page.Items) != 2 || page.Items[0].ID != "ntf_page_three" || page.Items[1].ID != "ntf_page_two" || page.NextCursor == "" || page.UnreadCount != 3 {
		t.Fatalf("first page = %#v", page)
	}
	next, err := db.ListUserNotifications(ctx, user.ID, domain.NotificationListOptions{Limit: 2, Cursor: page.NextCursor})
	if err != nil || len(next.Items) != 1 || next.Items[0].ID != "ntf_page_one" || next.NextCursor != "" {
		t.Fatalf("next page = %#v, err=%v", next, err)
	}
	if _, err := db.ListUserNotifications(ctx, user.ID, domain.NotificationListOptions{Cursor: "not-a-cursor"}); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("invalid cursor error = %v, want ErrInvalidCursor", err)
	}
}

func TestUserNotificationMutationsEnforceOwnership(t *testing.T) {
	t.Parallel()
	ctx, db, user, home, now := notificationStoreFixture(t, "ownership")
	defer db.Close()
	other := domain.User{ID: "usr_notifications_other", Email: "notifications-other@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateUser(ctx, other); err != nil {
		t.Fatalf("CreateUser other: %v", err)
	}
	item, _, err := db.CreateOrCoalesceUserNotification(ctx, notificationInput("ntf_owned", user.ID, home.ID, "event-owned", "owned", "changed", now))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MarkUserNotificationRead(ctx, other.ID, item.ID, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign mark error = %v, want ErrNotFound", err)
	}
	if err := db.DeleteUserNotification(ctx, other.ID, item.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign delete error = %v, want ErrNotFound", err)
	}
	if count, err := db.MarkAllUserNotificationsRead(ctx, user.ID, now.Add(time.Minute)); err != nil || count != 1 {
		t.Fatalf("MarkAllUserNotificationsRead = %d, %v", count, err)
	}
	if count, err := db.CountUnreadUserNotifications(ctx, user.ID); err != nil || count != 0 {
		t.Fatalf("CountUnreadUserNotifications = %d, %v", count, err)
	}
	if count, err := db.DeleteAllUserNotifications(ctx, user.ID); err != nil || count != 1 {
		t.Fatalf("DeleteAllUserNotifications = %d, %v", count, err)
	}
}

func notificationStoreFixture(t *testing.T, suffix string) (context.Context, *Store, domain.User, domain.Home, time.Time) {
	t.Helper()
	ctx := context.Background()
	db := openTestStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	user := domain.User{ID: "usr_notifications_" + suffix, Email: "notifications-" + suffix + "@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateUser(ctx, user); err != nil {
		db.Close()
		t.Fatalf("CreateUser: %v", err)
	}
	home := domain.Home{ID: "home_notifications_" + suffix, UserID: user.ID, Name: "Notification Home", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateHome(ctx, home); err != nil {
		db.Close()
		t.Fatalf("CreateHome: %v", err)
	}
	return ctx, db, user, home, now
}

func notificationInput(id, userID, homeID, eventKey, collapseKey, outcome string, occurredAt time.Time) domain.CreateUserNotificationInput {
	return domain.CreateUserNotificationInput{
		EventKey: eventKey,
		Notification: domain.UserNotification{
			ID:              id,
			UserID:          userID,
			HomeID:          homeID,
			Category:        domain.NotificationCategoryAgentHealth,
			EventKind:       "agent.offline",
			Severity:        "warning",
			Title:           "Agent offline",
			Body:            "A Hank Agent is offline.",
			TargetPath:      "/dashboard/agents/agent-one",
			CollapseKey:     collapseKey,
			Outcome:         outcome,
			FirstOccurredAt: occurredAt,
			LastOccurredAt:  occurredAt,
			CreatedAt:       occurredAt,
		},
	}
}
