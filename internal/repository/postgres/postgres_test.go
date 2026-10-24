package postgres

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/moixblau/HappyX.Api/internal/domain"
	"github.com/moixblau/HappyX.Api/internal/usecase"
)

// These tests need a real PostgreSQL. Set TEST_DATABASE_URL to run them, e.g.
// postgres://postgres:test@localhost:55432/happyx_test?sslmode=disable
func testService(t *testing.T, now time.Time) *usecase.Service {
	t.Helper()
	return testServiceClock(t, func() time.Time { return now })
}

func testServiceClock(t *testing.T, now usecase.Clock) *usecase.Service {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	reset(t, pool)
	return usecase.NewService(NewGuildRepo(pool), NewMemberRepo(pool), NewMoodRepo(pool), NewDispatchRepo(pool), NewKudosRepo(pool), NewGameRepo(pool), NewAccessRepo(pool), NewPrivacyRepo(pool), now)
}

func reset(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `TRUNCATE guilds, guild_removals, members, mood_entries, dispatches, kudos, game_rounds, game_answers, game_guesses RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
}

const (
	guildA = "100000000000000001"
	guildB = "100000000000000002"
	admin  = "300000000000000001"
	user1  = "200000000000000001"
	user2  = "200000000000000002"
)

func TestGuildLifecycle(t *testing.T) {
	ctx := context.Background()
	svc := testService(t, time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC))

	g, err := svc.RegisterGuild(ctx, usecase.RegisterGuildInput{DiscordID: guildA, Name: "A", AdminDiscordID: admin})
	if err != nil {
		t.Fatal(err)
	}
	if !g.Settings.Anonymous || !g.Active || g.Settings.WorkingDays != domain.MondayToFriday {
		t.Fatalf("new guild should be active, anonymous and work Monday to Friday: %+v", g)
	}

	off := false
	hour := 9
	sunToThu := domain.Weekdays(1<<time.Sunday | 1<<time.Monday | 1<<time.Tuesday | 1<<time.Wednesday | 1<<time.Thursday)
	if _, err := svc.UpdateGuildSettings(ctx, guildA, usecase.SettingsPatch{Anonymous: &off, SurveyHour: &hour, WorkingDays: &sunToThu}); err != nil {
		t.Fatal(err)
	}
	// Re-registering (e.g. bot restart) keeps settings.
	g, err = svc.RegisterGuild(ctx, usecase.RegisterGuildInput{DiscordID: guildA, Name: "A renamed", AdminDiscordID: admin})
	if err != nil {
		t.Fatal(err)
	}
	if g.Settings.Anonymous || g.Settings.SurveyHour != 9 || g.Settings.WorkingDays != sunToThu || g.Name != "A renamed" {
		t.Fatalf("settings lost on re-register: %+v", g)
	}

	if err := svc.RemoveGuild(ctx, guildA); err != nil {
		t.Fatal(err)
	}
	active, err := svc.ListActiveGuilds(ctx)
	if err != nil || len(active) != 0 {
		t.Fatalf("removed guild still active: %v %v", active, err)
	}
	if _, err := svc.UpsertMember(ctx, guildA, user1, "u1", nil, nil); !errors.Is(err, domain.ErrInactive) {
		t.Fatalf("join on inactive guild: want ErrInactive, got %v", err)
	}
}

func TestMoodUpsertOnePerDay(t *testing.T) {
	ctx := context.Background()
	svc := testService(t, time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC))
	mustRegister(t, svc, guildA)
	mustJoin(t, svc, guildA, user1)

	if _, err := svc.RecordMood(ctx, usecase.RecordMoodInput{GuildID: guildA, UserID: user1, Mood: domain.MoodBad}); err != nil {
		t.Fatal(err)
	}
	e, err := svc.RecordMood(ctx, usecase.RecordMoodInput{GuildID: guildA, UserID: user1, Mood: domain.MoodGood, Message: "better now"})
	if err != nil {
		t.Fatal(err)
	}
	if e.Mood != domain.MoodGood || e.Day != time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("unexpected entry: %+v", e)
	}
	r, err := svc.Report(ctx, guildA, e.Day, e.Day)
	if err != nil {
		t.Fatal(err)
	}
	if r.Responses != 1 || r.Average != float64(domain.MoodGood) {
		t.Fatalf("second answer should replace the first: %+v", r)
	}

	future := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	if _, err := svc.RecordMood(ctx, usecase.RecordMoodInput{GuildID: guildA, UserID: user1, Mood: domain.MoodGood, Day: future}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("future day: want ErrInvalid, got %v", err)
	}
	if _, err := svc.RecordMood(ctx, usecase.RecordMoodInput{GuildID: guildA, UserID: user2, Mood: domain.MoodGood}); !errors.Is(err, domain.ErrNotAMember) {
		t.Fatalf("non member: want ErrNotAMember, got %v", err)
	}
}

// TestAnswersKeepTheirAnonymity checks that turning anonymity off only names answers given
// from then on.
func TestAnswersKeepTheirAnonymity(t *testing.T) {
	ctx := context.Background()
	svc := testService(t, time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC))
	mustRegister(t, svc, guildA)
	mustJoin(t, svc, guildA, user1)
	mustJoin(t, svc, guildA, user2)
	day := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)

	record := func(user, msg string) domain.MoodEntry {
		t.Helper()
		e, err := svc.RecordMood(ctx, usecase.RecordMoodInput{GuildID: guildA, UserID: user, Mood: domain.MoodGood, Message: msg})
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	setAnonymous := func(v bool) {
		t.Helper()
		if _, err := svc.UpdateGuildSettings(ctx, guildA, usecase.SettingsPatch{Anonymous: &v}); err != nil {
			t.Fatal(err)
		}
	}
	authors := func() map[string]string {
		t.Helper()
		r, err := svc.Report(ctx, guildA, day, day)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, m := range r.Messages {
			out[m.Message] = m.MemberDiscordID
		}
		return out
	}

	if e := record(user1, "before"); !e.Anonymous {
		t.Fatalf("answer in an anonymous guild not marked anonymous: %+v", e)
	}
	setAnonymous(false)
	if e := record(user2, "after"); e.Anonymous {
		t.Fatalf("answer in a public guild marked anonymous: %+v", e)
	}
	if got := authors(); got["before"] != "" || got["after"] != user2 {
		t.Fatalf("authors after going public = %v, want only the new answer named", got)
	}

	// Answering again once the guild is public makes that answer public.
	record(user1, "before, edited")
	if got := authors(); got["before, edited"] != user1 {
		t.Fatalf("authors after re-answering = %v, want user1 named", got)
	}

	setAnonymous(true)
	for msg, id := range authors() {
		if id != "" {
			t.Fatalf("anonymous guild names the author of %q", msg)
		}
	}
}

// TestGuildIsolation checks that the same Discord user in two servers has two separate
// histories, and that one guild's data never shows up in another's.
func TestGuildIsolation(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	svc := testService(t, now)
	mustRegister(t, svc, guildA)
	mustRegister(t, svc, guildB)
	mustJoin(t, svc, guildA, user1)
	mustJoin(t, svc, guildB, user1)
	mustJoin(t, svc, guildA, user2)

	record := func(guild, user string, m domain.Mood) {
		t.Helper()
		if _, err := svc.RecordMood(ctx, usecase.RecordMoodInput{GuildID: guild, UserID: user, Mood: m, Message: guild}); err != nil {
			t.Fatal(err)
		}
	}
	record(guildA, user1, domain.MoodGreat)
	record(guildA, user2, domain.MoodGreat)
	record(guildB, user1, domain.MoodAwful)

	day := domain.TruncateDay(now)
	ra, err := svc.Report(ctx, guildA, day, day)
	if err != nil {
		t.Fatal(err)
	}
	rb, err := svc.Report(ctx, guildB, day, day)
	if err != nil {
		t.Fatal(err)
	}
	if ra.Responses != 2 || ra.Average != 5 {
		t.Fatalf("guild A report mixed data: %+v", ra)
	}
	if rb.Responses != 1 || rb.Average != 1 {
		t.Fatalf("guild B report mixed data: %+v", rb)
	}
	for _, m := range rb.Messages {
		if m.Message != guildB {
			t.Fatalf("guild B report contains another guild's message: %+v", m)
		}
	}

	membersB, err := svc.ListMembers(ctx, guildB)
	if err != nil || len(membersB) != 1 {
		t.Fatalf("guild B should have 1 member, got %v %v", membersB, err)
	}
	// user2 is only in guild A; it cannot answer in guild B.
	if _, err := svc.RecordMood(ctx, usecase.RecordMoodInput{GuildID: guildB, UserID: user2, Mood: domain.MoodGood}); !errors.Is(err, domain.ErrNotAMember) {
		t.Fatalf("cross-guild answer: want ErrNotAMember, got %v", err)
	}
	// Leaving guild A does not affect membership in guild B.
	if err := svc.LeaveMember(ctx, guildA, user1, false); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RecordMood(ctx, usecase.RecordMoodInput{GuildID: guildB, UserID: user1, Mood: domain.MoodGood}); err != nil {
		t.Fatalf("user should still be a member of guild B: %v", err)
	}
}

// TestKudosIsolation checks kudos stay in their guild and can't link members across guilds.
func TestKudosIsolation(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	svc := testService(t, now)
	mustRegister(t, svc, guildA)
	mustRegister(t, svc, guildB)
	mustJoin(t, svc, guildA, user1)
	mustJoin(t, svc, guildA, user2)
	mustJoin(t, svc, guildB, user1)

	give := func(guild, from, to string) error {
		_, err := svc.GiveKudos(ctx, usecase.GiveKudosInput{GuildID: guild, FromUserID: from, ToUserID: to, Message: "gracias " + guild})
		return err
	}
	if err := give(guildA, user1, user2); err != nil {
		t.Fatal(err)
	}
	// user2 is only in guild A.
	if err := give(guildB, user1, user2); !errors.Is(err, domain.ErrNotAMember) {
		t.Fatalf("cross-guild kudos: want ErrNotAMember, got %v", err)
	}
	// Pausing the survey doesn't stop anyone from getting kudos.
	if err := svc.LeaveMember(ctx, guildA, user2, false); err != nil {
		t.Fatal(err)
	}
	if err := give(guildA, user1, user2); err != nil {
		t.Fatalf("kudos to a paused member: %v", err)
	}

	day := domain.TruncateDay(now)
	ra, err := svc.Report(ctx, guildA, day, day)
	if err != nil {
		t.Fatal(err)
	}
	rb, err := svc.Report(ctx, guildB, day, day)
	if err != nil {
		t.Fatal(err)
	}
	if ra.Kudos.Count != 2 || ra.Kudos.Highlights[0].ReceiverUsername != "user2" || ra.Kudos.Highlights[0].Message != "gracias "+guildA {
		t.Fatalf("guild A kudos: %+v", ra.Kudos)
	}
	if rb.Kudos.Count != 0 {
		t.Fatalf("guild B report shows another guild's kudos: %+v", rb.Kudos)
	}
}

func mustRegister(t *testing.T, svc *usecase.Service, guild string) {
	t.Helper()
	if _, err := svc.RegisterGuild(context.Background(), usecase.RegisterGuildInput{DiscordID: guild, Name: "g" + guild[len(guild)-1:], AdminDiscordID: admin}); err != nil {
		t.Fatal(err)
	}
}

func mustJoin(t *testing.T, svc *usecase.Service, guild, user string) {
	t.Helper()
	if _, err := svc.UpsertMember(context.Background(), guild, user, "user"+user[len(user)-1:], nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestSyncMembers(t *testing.T) {
	ctx := context.Background()
	svc := testService(t, time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC))
	mustRegister(t, svc, guildA)
	mustRegister(t, svc, guildB)
	mustJoin(t, svc, guildA, user1)
	if err := svc.LeaveMember(ctx, guildA, user1, false); err != nil {
		t.Fatal(err)
	}
	role := "400000000000000001"
	err := svc.SyncMembers(ctx, guildA, []domain.Member{
		{DiscordID: user1, Username: "renamed", RoleIDs: []string{role}},
		{DiscordID: user2, Username: "old"},
		{DiscordID: user2, Username: "new"}, // listed twice: the last one wins
	})
	if err != nil {
		t.Fatal(err)
	}
	m, err := svc.UpsertMember(ctx, guildA, user1, "renamed", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.Active || len(m.RoleIDs) != 1 || m.RoleIDs[0] != role {
		t.Fatalf("sync should set the roles and keep the opt-out: %+v", m)
	}
	active, err := svc.ListMembers(ctx, guildA)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].DiscordID != user2 || active[0].Username != "new" {
		t.Fatalf("new members are opted in: %+v", active)
	}
	if other, _ := svc.ListMembers(ctx, guildB); len(other) != 0 {
		t.Fatalf("another guild got members: %+v", other)
	}
	if err := svc.SyncMembers(ctx, guildA, []domain.Member{{DiscordID: "nope"}}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("bad ID: want ErrInvalid, got %v", err)
	}
}

func TestClaimDispatchOnce(t *testing.T) {
	ctx := context.Background()
	svc := testService(t, time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC))
	mustRegister(t, svc, guildA)
	mustRegister(t, svc, guildB)
	day := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)

	claim := func(guild string, kind domain.DispatchKind) bool {
		t.Helper()
		ok, err := svc.ClaimDispatch(ctx, guild, kind, day)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !claim(guildA, domain.DispatchSurvey) {
		t.Fatal("first claim should succeed")
	}
	if claim(guildA, domain.DispatchSurvey) {
		t.Fatal("second claim for the same day should fail")
	}
	if !claim(guildA, domain.DispatchReport) || !claim(guildB, domain.DispatchSurvey) {
		t.Fatal("claims for another kind or guild are independent")
	}
	if _, err := svc.ClaimDispatch(ctx, guildA, "spam", day); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("bad kind: want ErrInvalid, got %v", err)
	}
}

func TestSyncKeepsOptOut(t *testing.T) {
	ctx := context.Background()
	svc := testService(t, time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC))
	mustRegister(t, svc, guildA)
	mustJoin(t, svc, guildA, user1)
	if err := svc.LeaveMember(ctx, guildA, user1, false); err != nil {
		t.Fatal(err)
	}
	// A member list sync (active nil) must not re-subscribe them.
	m, err := svc.UpsertMember(ctx, guildA, user1, "renamed", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.Active || m.Username != "renamed" {
		t.Fatalf("sync should update the name but keep the opt-out: %+v", m)
	}
	yes := true
	if m, err = svc.UpsertMember(ctx, guildA, user1, "renamed", &yes, nil); err != nil || !m.Active {
		t.Fatalf("explicit opt-in should reactivate: %+v %v", m, err)
	}
}
