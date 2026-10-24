// Package usecase implements HappyX's application logic. It depends only on the domain
// and on the repository interfaces declared here; storage lives in adapters.
package usecase

import (
	"context"
	"time"

	"github.com/moixblau/HappyX.Api/internal/domain"
)

// Every repository method that reads or writes tenant data takes the guild's Discord ID,
// so implementations can (and must) scope each query to a single guild.

type GuildRepository interface {
	// Upsert creates the guild or, if it exists, updates its name and reactivates it. The
	// admin and settings of an existing guild are kept.
	Upsert(ctx context.Context, g domain.Guild) (domain.Guild, error)
	Get(ctx context.Context, guildID string) (domain.Guild, error)
	ListActive(ctx context.Context) ([]domain.Guild, error)
	// ListVisible returns the active guilds whose admin is userID, whose Discord ID is in
	// guildIDs, or where userID is in the server and has a grant, directly or through a role.
	ListVisible(ctx context.Context, userID string, guildIDs []string) ([]domain.Guild, error)
	// ListJoined returns the active guilds where userID is a member taking part (not paused).
	ListJoined(ctx context.Context, userID string) ([]domain.Guild, error)
	// UpdateConfig sets the guild's admin and settings in one write.
	UpdateConfig(ctx context.Context, guildID, adminID string, s domain.GuildSettings) (domain.Guild, error)
	// Deactivate marks the guild as left by the bot at the given time. Its data is kept until
	// DeleteLeftBefore.
	Deactivate(ctx context.Context, guildID string, at time.Time) error
	// Delete removes the guild and all its data, and records it in the removal queue so the
	// bot leaves the server.
	Delete(ctx context.Context, guildID string) error
	// DeleteLeftBefore deletes, with all their data, the guilds the bot left before the given
	// time, and returns their Discord IDs.
	DeleteLeftBefore(ctx context.Context, before time.Time) ([]string, error)
	// ListRemovals returns the deleted guilds the bot still has to leave.
	ListRemovals(ctx context.Context) ([]string, error)
	// Removing reports whether the guild is in the removal queue.
	Removing(ctx context.Context, guildID string) (bool, error)
	// DoneRemoving takes the guild off the removal queue.
	DoneRemoving(ctx context.Context, guildID string) error
}

type MemberRepository interface {
	// Upsert creates the member (active unless setActive says otherwise) or updates its
	// username. An existing member's active flag only changes when setActive is non-nil, so a
	// bulk sync never re-subscribes someone who opted out.
	Upsert(ctx context.Context, m domain.Member, setActive *bool) (domain.Member, error)
	// SyncMany upserts members of one guild in a single transaction, refreshing their username
	// and roles. Existing members keep their opt-in state; new ones are opted in.
	SyncMany(ctx context.Context, guildID string, ms []domain.Member) error
	Get(ctx context.Context, guildID, userID string) (domain.Member, error)
	ListActive(ctx context.Context, guildID string) ([]domain.Member, error)
	// ListInServer returns the members still in the Discord server, opted in or not, by name.
	ListInServer(ctx context.Context, guildID string) ([]domain.Member, error)
	Deactivate(ctx context.Context, guildID, userID string) error
	// Left deactivates the member and records they left the Discord server.
	Left(ctx context.Context, guildID, userID string) error
}

type AccessRepository interface {
	// ReplaceRoles sets the guild's roles and drops the grants to roles that no longer exist.
	ReplaceRoles(ctx context.Context, guildID string, roles []domain.Role) error
	// ListRoles returns the guild's roles, highest first as in Discord.
	ListRoles(ctx context.Context, guildID string) ([]domain.Role, error)
	ListGrants(ctx context.Context, guildID string) ([]domain.AccessGrant, error)
	// SetAccess sets the guild's main admin and replaces its grants in one write.
	SetAccess(ctx context.Context, guildID, adminID string, grants []domain.AccessGrant) error
}

type DispatchRepository interface {
	// Claim records that kind was sent to the guild for period. It returns false if it
	// had already been claimed.
	Claim(ctx context.Context, guildID string, kind domain.DispatchKind, period time.Time) (bool, error)
}

type MoodRepository interface {
	// Upsert stores the member's mood for e.Day, replacing an earlier answer for that day.
	Upsert(ctx context.Context, e domain.MoodEntry) (domain.MoodEntry, error)
	ListBetween(ctx context.Context, guildID string, from, to time.Time) ([]domain.MoodEntry, error)
	// ListForUser returns one person's own answers in every guild, oldest first.
	ListForUser(ctx context.Context, userID string, from, to time.Time) ([]domain.MoodEntry, error)
	// Streaks returns each active member's answering streak up to today (see domain.Streak),
	// reading answers from `from`. Members without one are left out.
	Streaks(ctx context.Context, guildID string, workingDays domain.Weekdays, from, today time.Time) (map[string]int, error)
}

type KudosRepository interface {
	// Create stores kudos between two existing members of the guild and returns it with both
	// usernames filled in.
	Create(ctx context.Context, k domain.Kudos) (domain.Kudos, error)
	CountGivenOn(ctx context.Context, guildID, giverID string, day time.Time) (int, error)
	// ListBetween returns the guild's kudos in [from, to], oldest first.
	ListBetween(ctx context.Context, guildID string, from, to time.Time) ([]domain.Kudos, error)
}

type GameRepository interface {
	// CreateRound stores the guild's round for r.Week, or returns the existing one unchanged.
	// r.ProposerDiscordID, when set, names the member whose proposal it is.
	CreateRound(ctx context.Context, r domain.GameRound) (domain.GameRound, error)
	GetRound(ctx context.Context, guildID string, week time.Time) (domain.GameRound, error)
	// UpsertAnswer stores the member's answer to the round, replacing an earlier one.
	UpsertAnswer(ctx context.Context, a domain.GameAnswer) (domain.GameAnswer, error)
	// ListAnswers returns the round's answers by members still taking part, in a random but
	// stable order, with their usernames.
	ListAnswers(ctx context.Context, guildID string, week time.Time) ([]domain.GameAnswer, error)
	// ReplaceGuesses replaces everything guesserID guessed in the round with guesses.
	ReplaceGuesses(ctx context.Context, guildID string, week time.Time, guesserID string, guesses []domain.GameGuess) error
	// ListGuesses returns the round's guesses by members still taking part.
	ListGuesses(ctx context.Context, guildID string, week time.Time) ([]domain.GameGuess, error)
	// UpsertProposal stores the member's question for p.Week, replacing an earlier one.
	UpsertProposal(ctx context.Context, p domain.GameProposal) (domain.GameProposal, error)
	// ListProposals returns the questions proposed for week by members still taking part.
	ListProposals(ctx context.Context, guildID string, week time.Time) ([]domain.GameProposal, error)
}

type PrivacyRepository interface {
	// UserData returns everything stored about userID, in every guild, including guilds the bot
	// left that are still kept.
	UserData(ctx context.Context, userID string) (domain.UserData, error)
	// EraseUser deletes userID's answers, kudos given and received, and everything they did in
	// «Who's who?», in every guild. Their member rows stay, paused and without a username, so
	// the bot doesn't send them the survey again; Discord IDs are kept only for that.
	EraseUser(ctx context.Context, userID string) error
	// GuildData returns everything stored about the guild.
	GuildData(ctx context.Context, guildID string) (domain.GuildData, error)
}

// Clock lets tests control "today".
type Clock func() time.Time
