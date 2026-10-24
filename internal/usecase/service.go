package usecase

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/moixblau/HappyX.Api/internal/domain"
)

type Service struct {
	guilds     GuildRepository
	members    MemberRepository
	moods      MoodRepository
	dispatches DispatchRepository
	kudos      KudosRepository
	games      GameRepository
	access     AccessRepository
	privacy    PrivacyRepository
	now        Clock
}

func NewService(g GuildRepository, m MemberRepository, mo MoodRepository, d DispatchRepository, k KudosRepository, ga GameRepository, a AccessRepository, p PrivacyRepository, now Clock) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{guilds: g, members: m, moods: mo, dispatches: d, kudos: k, games: ga, access: a, privacy: p, now: now}
}

// --- Guilds ---

type RegisterGuildInput struct {
	DiscordID      string
	Name           string
	AdminDiscordID string
}

// RegisterGuild is called when the bot joins a server (or on startup, for servers it is in).
func (s *Service) RegisterGuild(ctx context.Context, in RegisterGuildInput) (domain.Guild, error) {
	g := domain.Guild{
		DiscordID:      in.DiscordID,
		Name:           in.Name,
		AdminDiscordID: in.AdminDiscordID,
		Settings:       domain.DefaultGuildSettings(),
		Active:         true,
	}
	if err := g.Validate(); err != nil {
		return domain.Guild{}, err
	}
	// A server deleted from the dashboard must not come back before the bot has left it.
	if removing, err := s.guilds.Removing(ctx, in.DiscordID); err != nil {
		return domain.Guild{}, err
	} else if removing {
		return domain.Guild{}, domain.ErrRemoving
	}
	return s.guilds.Upsert(ctx, g)
}

func (s *Service) GetGuild(ctx context.Context, guildID string) (domain.Guild, error) {
	return s.guilds.Get(ctx, guildID)
}

func (s *Service) ListActiveGuilds(ctx context.Context) ([]domain.Guild, error) {
	return s.guilds.ListActive(ctx)
}

// SettingsPatch carries optional changes; nil fields are left untouched.
type SettingsPatch struct {
	Anonymous       *bool
	Timezone        *string
	Language        *string
	SurveyHour      *int
	ReportFrequency *domain.ReportFrequency
	AdminDiscordID  *string
	// KudosChannelID set to "" goes back to posting where the command is used.
	KudosChannelID *string
	GamesEnabled   *bool
	WorkingDays    *domain.Weekdays
	// SurveyMessage set to "" goes back to the default survey text.
	SurveyMessage *string
}

func (s *Service) UpdateGuildSettings(ctx context.Context, guildID string, p SettingsPatch) (domain.Guild, error) {
	g, err := s.activeGuild(ctx, guildID)
	if err != nil {
		return domain.Guild{}, err
	}
	st := g.Settings
	if p.Anonymous != nil {
		st.Anonymous = *p.Anonymous
	}
	if p.Timezone != nil {
		st.Timezone = *p.Timezone
	}
	if p.Language != nil {
		st.Language = *p.Language
	}
	if p.SurveyHour != nil {
		st.SurveyHour = *p.SurveyHour
	}
	if p.ReportFrequency != nil {
		st.ReportFrequency = *p.ReportFrequency
	}
	if p.KudosChannelID != nil {
		st.KudosChannelID = *p.KudosChannelID
	}
	if p.GamesEnabled != nil {
		st.GamesEnabled = *p.GamesEnabled
	}
	if p.WorkingDays != nil {
		st.WorkingDays = *p.WorkingDays
	}
	if p.SurveyMessage != nil {
		st.SurveyMessage = strings.TrimSpace(*p.SurveyMessage)
	}
	if p.AdminDiscordID != nil {
		g.AdminDiscordID = *p.AdminDiscordID
	}
	g.Settings = st
	if err := g.Validate(); err != nil {
		return domain.Guild{}, err
	}
	return s.guilds.UpdateConfig(ctx, guildID, g.AdminDiscordID, st)
}

// LeftGuildRetention is how long a guild's data is kept after the bot is removed, so adding the
// bot back brings everything back.
const LeftGuildRetention = 30 * 24 * time.Hour

// RemoveGuild is called when the bot is kicked. The guild stops receiving surveys and reports
// and disappears from the dashboard; its data is deleted after LeftGuildRetention.
func (s *Service) RemoveGuild(ctx context.Context, guildID string) error {
	return s.guilds.Deactivate(ctx, guildID, s.now())
}

// DeleteLeftGuilds deletes the guilds the bot left more than LeftGuildRetention ago, with all
// their data, and returns their Discord IDs.
func (s *Service) DeleteLeftGuilds(ctx context.Context) ([]string, error) {
	return s.guilds.DeleteLeftBefore(ctx, s.now().Add(-LeftGuildRetention))
}

// DeleteGuild deletes a guild and all its data from the dashboard. confirmName must be the
// guild's name, as the viewer typed it to confirm. The bot then leaves the server.
func (s *Service) DeleteGuild(ctx context.Context, v domain.Viewer, guildID, confirmName string) error {
	g, err := s.ViewerGuild(ctx, v, guildID, domain.AccessOwner)
	if err != nil {
		return err
	}
	if strings.TrimSpace(confirmName) != strings.TrimSpace(g.Name) {
		return domain.ValidationError{Reason: "type the server's name to confirm"}
	}
	return s.guilds.Delete(ctx, guildID)
}

// GuildRemovals lists the guilds deleted from the dashboard that the bot still has to leave.
func (s *Service) GuildRemovals(ctx context.Context) ([]string, error) {
	return s.guilds.ListRemovals(ctx)
}

// GuildRemoved is called once the bot has left a deleted guild.
func (s *Service) GuildRemoved(ctx context.Context, guildID string) error {
	return s.guilds.DoneRemoving(ctx, guildID)
}

// --- Members ---

// UpsertMember registers a guild member or refreshes their username and roles. With active nil
// an existing member keeps their opt-in state (used when syncing the member list); true opts
// them in (they joined the server or asked to receive the survey again). With roleIDs nil
// their stored roles are kept.
func (s *Service) UpsertMember(ctx context.Context, guildID, userID, username string, active *bool, roleIDs []string) (domain.Member, error) {
	if _, err := s.activeGuild(ctx, guildID); err != nil {
		return domain.Member{}, err
	}
	m := domain.Member{GuildDiscordID: guildID, DiscordID: userID, Username: username, RoleIDs: roleIDs}
	if err := m.Validate(); err != nil {
		return domain.Member{}, err
	}
	return s.members.Upsert(ctx, m, active)
}

// MaxSyncMembers is how many members one SyncMembers call takes: a page of Discord's member list.
const MaxSyncMembers = 1000

// SyncMembers registers members or refreshes their usernames and roles in one write, keeping
// everyone's opt-in state. The bot sends its member list this way when it connects, a page at a
// time, instead of one request and one commit per member.
func (s *Service) SyncMembers(ctx context.Context, guildID string, ms []domain.Member) error {
	if _, err := s.activeGuild(ctx, guildID); err != nil {
		return err
	}
	if len(ms) > MaxSyncMembers {
		return domain.ValidationError{Reason: fmt.Sprintf("at most %d members per sync", MaxSyncMembers)}
	}
	// A member listed twice would make the upsert touch the same row twice; the last one wins.
	seen := make(map[string]int, len(ms))
	out := make([]domain.Member, 0, len(ms))
	for _, m := range ms {
		m.GuildDiscordID = guildID
		if err := m.Validate(); err != nil {
			return err
		}
		if m.RoleIDs == nil {
			m.RoleIDs = []string{}
		}
		if i, ok := seen[m.DiscordID]; ok {
			out[i] = m
			continue
		}
		seen[m.DiscordID] = len(out)
		out = append(out, m)
	}
	if len(out) == 0 {
		return nil
	}
	return s.members.SyncMany(ctx, guildID, out)
}

// LeaveMember stops sending the survey to the member. With leftServer they also left the
// Discord server, which ends any access they had to its dashboard.
func (s *Service) LeaveMember(ctx context.Context, guildID, userID string, leftServer bool) error {
	if leftServer {
		return s.members.Left(ctx, guildID, userID)
	}
	return s.members.Deactivate(ctx, guildID, userID)
}

func (s *Service) ListMembers(ctx context.Context, guildID string) ([]domain.Member, error) {
	if _, err := s.activeGuild(ctx, guildID); err != nil {
		return nil, err
	}
	return s.members.ListActive(ctx, guildID)
}

// MemberStreaks returns, for each member who has one, how many days in a row up to today they
// answered the survey in the guild. The bot uses it to fill {{user.streak}} in a member's own DM.
func (s *Service) MemberStreaks(ctx context.Context, guildID string) (map[string]int, error) {
	g, err := s.activeGuild(ctx, guildID)
	if err != nil {
		return nil, err
	}
	w := g.Settings.WorkingDays
	if w == 0 {
		w = domain.MondayToFriday
	}
	today := s.today(g)
	return s.moods.Streaks(ctx, guildID, w, today.AddDate(0, 0, -streakWindow), today)
}

// --- Moods ---

type RecordMoodInput struct {
	GuildID string
	UserID  string
	Mood    domain.Mood
	Message string
	// Day is optional; when zero, today in the guild's timezone is used.
	Day time.Time
}

func (s *Service) RecordMood(ctx context.Context, in RecordMoodInput) (domain.MoodEntry, error) {
	g, err := s.activeGuild(ctx, in.GuildID)
	if err != nil {
		return domain.MoodEntry{}, err
	}
	m, err := s.members.Get(ctx, in.GuildID, in.UserID)
	if err != nil || !m.Active {
		if err == nil || err == domain.ErrNotFound {
			return domain.MoodEntry{}, domain.ErrNotAMember
		}
		return domain.MoodEntry{}, err
	}
	today := s.today(g)
	day := in.Day
	if day.IsZero() {
		day = today
	}
	day = domain.TruncateDay(day)
	// Late answers to yesterday's survey are fine; the future is not.
	if day.After(today) {
		return domain.MoodEntry{}, domain.ValidationError{Reason: "day cannot be in the future"}
	}
	e := domain.MoodEntry{
		GuildDiscordID:  in.GuildID,
		MemberDiscordID: in.UserID,
		Day:             day,
		Mood:            in.Mood,
		Message:         in.Message,
	}
	if err := e.Validate(); err != nil {
		return domain.MoodEntry{}, err
	}
	return s.moods.Upsert(ctx, e)
}

// --- Kudos ---

type GiveKudosInput struct {
	GuildID    string
	FromUserID string
	ToUserID   string
	Message    string
}

// GiveKudos records public thanks between two members of the guild. Both must be known
// members, though not necessarily taking part in the survey: pausing the survey doesn't stop
// anyone from giving or getting kudos.
func (s *Service) GiveKudos(ctx context.Context, in GiveKudosInput) (domain.Kudos, error) {
	g, err := s.activeGuild(ctx, in.GuildID)
	if err != nil {
		return domain.Kudos{}, err
	}
	k := domain.Kudos{
		GuildDiscordID:    in.GuildID,
		GiverDiscordID:    in.FromUserID,
		ReceiverDiscordID: in.ToUserID,
		Day:               s.today(g),
		Message:           in.Message,
	}
	if err := k.Validate(); err != nil {
		return domain.Kudos{}, err
	}
	for _, id := range []string{in.FromUserID, in.ToUserID} {
		if _, err := s.members.Get(ctx, in.GuildID, id); err != nil {
			if err == domain.ErrNotFound {
				return domain.Kudos{}, domain.ErrNotAMember
			}
			return domain.Kudos{}, err
		}
	}
	n, err := s.kudos.CountGivenOn(ctx, in.GuildID, in.FromUserID, k.Day)
	if err != nil {
		return domain.Kudos{}, err
	}
	if n >= domain.MaxKudosPerDay {
		return domain.Kudos{}, domain.ErrTooMany
	}
	return s.kudos.Create(ctx, k)
}

// --- Dispatches ---

// ClaimDispatch lets the bot reserve sending a survey or report for a period exactly once.
// It returns false when it was already sent.
func (s *Service) ClaimDispatch(ctx context.Context, guildID string, kind domain.DispatchKind, period time.Time) (bool, error) {
	if !kind.Valid() {
		return false, domain.ValidationError{Reason: "kind must be \"survey\", \"report\", \"game\" or \"game_guess\""}
	}
	if period.IsZero() {
		return false, domain.ValidationError{Reason: "period is required"}
	}
	if _, err := s.activeGuild(ctx, guildID); err != nil {
		return false, err
	}
	return s.dispatches.Claim(ctx, guildID, kind, domain.TruncateDay(period))
}

// --- Reports ---

const maxReportDays = 366

func (s *Service) Report(ctx context.Context, guildID string, from, to time.Time) (domain.Report, error) {
	g, err := s.guilds.Get(ctx, guildID)
	if err != nil {
		return domain.Report{}, err
	}
	from, to = domain.TruncateDay(from), domain.TruncateDay(to)
	if to.Before(from) {
		return domain.Report{}, domain.ValidationError{Reason: "to must not be before from"}
	}
	if to.Sub(from) > maxReportDays*24*time.Hour {
		return domain.Report{}, domain.ValidationError{Reason: fmt.Sprintf("range must be at most %d days", maxReportDays)}
	}
	entries, err := s.moods.ListBetween(ctx, guildID, from, to)
	if err != nil {
		return domain.Report{}, err
	}
	usernames := map[string]string{}
	if !g.Settings.Anonymous {
		ms, err := s.members.ListActive(ctx, guildID)
		if err != nil {
			return domain.Report{}, err
		}
		for _, m := range ms {
			usernames[m.DiscordID] = m.Username
		}
	}
	kudos, err := s.kudos.ListBetween(ctx, guildID, from, to)
	if err != nil {
		return domain.Report{}, err
	}
	r := domain.BuildReport(g, from, to, entries, usernames)
	r.Kudos = domain.SummarizeKudos(kudos)
	return r, nil
}

// --- Web dashboard ---

// GuildAccess is a guild together with what the viewer may do with it.
type GuildAccess struct {
	Guild domain.Guild
	Level domain.AccessLevel
}

// VisibleGuilds lists the guilds the viewer may see on the dashboard, with their access level.
func (s *Service) VisibleGuilds(ctx context.Context, v domain.Viewer) ([]GuildAccess, error) {
	if v.DiscordID == "" {
		return nil, nil
	}
	gs, err := s.guilds.ListVisible(ctx, v.DiscordID, v.ManagedGuilds)
	if err != nil {
		return nil, err
	}
	out := make([]GuildAccess, 0, len(gs))
	for _, g := range gs {
		level, err := s.accessOf(ctx, g, v.DiscordID, v.ManagesInDiscord(g.DiscordID))
		if err != nil {
			return nil, err
		}
		if level >= domain.AccessView {
			out = append(out, GuildAccess{g, level})
		}
	}
	return out, nil
}

// streakWindow is how far back answers are read to count a streak.
const streakWindow = 400

// Evolution returns the viewer's own answers in [from, to], one summary per guild they have
// answered in, ordered by guild name. Any logged-in person can see their own answers, in every
// guild, whether or not they administer it.
func (s *Service) Evolution(ctx context.Context, v domain.Viewer, from, to time.Time) ([]domain.Evolution, error) {
	if v.DiscordID == "" {
		return nil, nil
	}
	from, to = domain.TruncateDay(from), domain.TruncateDay(to)
	if to.Before(from) {
		return nil, domain.ValidationError{Reason: "to must not be before from"}
	}
	if to.Sub(from) > maxReportDays*24*time.Hour {
		return nil, domain.ValidationError{Reason: fmt.Sprintf("range must be at most %d days", maxReportDays)}
	}
	// Read back far enough to count today's streak, and up to tomorrow so a guild whose
	// timezone is ahead of UTC still sees today's answer.
	nowDay := domain.TruncateDay(s.now().UTC())
	readFrom, readTo := from, to
	if w := nowDay.AddDate(0, 0, -streakWindow); w.Before(readFrom) {
		readFrom = w
	}
	if t := nowDay.AddDate(0, 0, 1); t.After(readTo) {
		readTo = t
	}
	entries, err := s.moods.ListForUser(ctx, v.DiscordID, readFrom, readTo)
	if err != nil {
		return nil, err
	}
	byGuild := map[string][]domain.MoodEntry{}
	var order []string
	for _, e := range entries {
		if _, ok := byGuild[e.GuildDiscordID]; !ok {
			order = append(order, e.GuildDiscordID)
		}
		byGuild[e.GuildDiscordID] = append(byGuild[e.GuildDiscordID], e)
	}
	out := make([]domain.Evolution, 0, len(order))
	for _, id := range order {
		g, err := s.guilds.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		ev := domain.BuildEvolution(g, byGuild[id], from, to, s.today(g))
		if ev.Responses > 0 || ev.Streak > 0 {
			out = append(out, ev)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if a, b := strings.ToLower(out[i].Guild.Name), strings.ToLower(out[j].Guild.Name); a != b {
			return a < b
		}
		return out[i].Guild.DiscordID < out[j].Guild.DiscordID
	})
	return out, nil
}

// ViewerGuild returns the guild if the viewer has at least the need level in it. Guilds they
// can't see at all are reported as not found, so the dashboard doesn't reveal which servers use
// HappyX; guilds they can see but not act on this way give ErrForbidden.
func (s *Service) ViewerGuild(ctx context.Context, v domain.Viewer, guildID string, need domain.AccessLevel) (domain.Guild, error) {
	g, err := s.guilds.Get(ctx, guildID)
	if err != nil {
		return domain.Guild{}, err
	}
	// A server the bot left is hidden from the dashboard until the bot is added back.
	if !g.Active {
		return domain.Guild{}, domain.ErrNotFound
	}
	level, err := s.accessOf(ctx, g, v.DiscordID, v.ManagesInDiscord(g.DiscordID))
	if err != nil {
		return domain.Guild{}, err
	}
	if level < domain.AccessView {
		return domain.Guild{}, domain.ErrNotFound
	}
	if level < need {
		return domain.Guild{}, domain.ErrForbidden
	}
	return g, nil
}

// LastPeriod returns the most recent complete week (Mon-Sun) or month before now,
// in the guild's timezone.
func (s *Service) LastPeriod(g domain.Guild) (from, to time.Time) {
	today := s.today(g)
	if g.Settings.ReportFrequency == domain.ReportMonthly {
		firstThisMonth := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)
		return firstThisMonth.AddDate(0, -1, 0), firstThisMonth.AddDate(0, 0, -1)
	}
	offset := (int(today.Weekday()) + 6) % 7 // days since Monday
	thisMonday := today.AddDate(0, 0, -offset)
	return thisMonday.AddDate(0, 0, -7), thisMonday.AddDate(0, 0, -1)
}

func (s *Service) today(g domain.Guild) time.Time {
	loc, err := time.LoadLocation(g.Settings.Timezone)
	if err != nil {
		loc = time.UTC
	}
	return domain.TruncateDay(s.now().In(loc))
}

func (s *Service) activeGuild(ctx context.Context, guildID string) (domain.Guild, error) {
	g, err := s.guilds.Get(ctx, guildID)
	if err != nil {
		return domain.Guild{}, err
	}
	if !g.Active {
		return domain.Guild{}, domain.ErrInactive
	}
	return g, nil
}
