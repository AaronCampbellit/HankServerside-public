package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

func TestConsumeAgentEnrollmentIsSingleUse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close()
	now := time.Now().UTC().Truncate(time.Microsecond)
	user := domain.User{ID: "usr_linux_enroll", Email: "linux-enroll@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_linux_enroll", UserID: user.ID, Name: "Linux Enrollment", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateHome(ctx, home); err != nil {
		t.Fatal(err)
	}
	enrollment := domain.AgentEnrollment{ID: "aenroll_fixture", HomeID: home.ID, Platform: "linux", TokenHash: strings.Repeat("a", 64), CreatedByUserID: user.ID, CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute)}
	if err := db.CreateAgentEnrollment(ctx, enrollment); err != nil {
		t.Fatal(err)
	}
	input := ConsumeAgentEnrollmentInput{TokenHash: enrollment.TokenHash, AgentID: "linux_fixture", Name: "fixture", CredentialID: "agtok_fixture", CredentialHash: strings.Repeat("b", 64), Now: now.Add(time.Minute)}
	result, err := db.ConsumeAgentEnrollment(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.AgentID != input.AgentID || result.ConsumedAt == nil {
		t.Fatalf("result = %#v", result)
	}
	if _, err := db.ConsumeAgentEnrollment(ctx, input); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second consume = %v", err)
	}
	if _, err := db.ValidateAgentToken(ctx, input.CredentialHash); err != nil {
		t.Fatalf("credential not usable: %v", err)
	}
}

func TestExpiredOrRevokedAgentEnrollmentCannotBeConsumed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close()
	now := time.Now().UTC().Truncate(time.Microsecond)
	user := domain.User{ID: "usr_linux_expired", Email: "linux-expired@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_linux_expired", UserID: user.ID, Name: "Expired", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateHome(ctx, home); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		id, hash string
		expires  time.Time
		revoke   bool
	}{
		{"expired", strings.Repeat("c", 64), now.Add(-time.Minute), false},
		{"revoked", strings.Repeat("d", 64), now.Add(time.Minute), true},
	} {
		e := domain.AgentEnrollment{ID: "aenroll_" + fixture.id, HomeID: home.ID, Platform: "linux", TokenHash: fixture.hash, CreatedByUserID: user.ID, CreatedAt: now.Add(-2 * time.Minute), ExpiresAt: fixture.expires}
		if err := db.CreateAgentEnrollment(ctx, e); err != nil {
			t.Fatal(err)
		}
		if fixture.revoke {
			if err := db.RevokeAgentEnrollment(ctx, home.ID, e.ID, now); err != nil {
				t.Fatal(err)
			}
		}
		_, err := db.ConsumeAgentEnrollment(ctx, ConsumeAgentEnrollmentInput{TokenHash: e.TokenHash, AgentID: "linux_" + fixture.id, Name: fixture.id, CredentialID: "agtok_" + fixture.id, CredentialHash: strings.Repeat("e", 64), Now: now})
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s consume = %v", fixture.id, err)
		}
	}
}

func TestConcurrentAgentEnrollmentConsumptionCreatesOneAgent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close()
	now := time.Now().UTC().Truncate(time.Microsecond)
	user := domain.User{ID: "usr_linux_concurrent", Email: "linux-concurrent@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_linux_concurrent", UserID: user.ID, Name: "Concurrent", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateHome(ctx, home); err != nil {
		t.Fatal(err)
	}
	enrollment := domain.AgentEnrollment{ID: "aenroll_concurrent", HomeID: home.ID, Platform: "linux", TokenHash: strings.Repeat("f", 64), CreatedByUserID: user.ID, CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute)}
	if err := db.CreateAgentEnrollment(ctx, enrollment); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	for index := 0; index < 2; index++ {
		index := index
		go func() {
			<-start
			_, err := db.ConsumeAgentEnrollment(ctx, ConsumeAgentEnrollmentInput{
				TokenHash: enrollment.TokenHash, AgentID: "linux_concurrent_0000000" + string(rune('0'+index)), Name: "concurrent",
				CredentialID: "agtok_concurrent_" + string(rune('0'+index)), CredentialHash: strings.Repeat(string(rune('a'+index)), 64), Now: now.Add(time.Minute),
			})
			results <- err
		}()
	}
	close(start)
	var successes, notFound int
	for index := 0; index < 2; index++ {
		err := <-results
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrNotFound):
			notFound++
		default:
			t.Fatalf("unexpected consume error: %v", err)
		}
	}
	if successes != 1 || notFound != 1 {
		t.Fatalf("successes=%d not_found=%d, want 1/1", successes, notFound)
	}
}
