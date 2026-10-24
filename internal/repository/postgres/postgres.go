// Package postgres implements the usecase repositories on PostgreSQL.
//
// Tenant isolation: every query on members and mood_entries joins or filters on the
// guild's Discord ID, so a caller can only ever see rows of the guild it names.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/golang-migrate/migrate/v4"
	pgxmigrate "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/moixblau/HappyX.Api/internal/domain"
	"github.com/moixblau/HappyX.Api/migrations"
)

// Connect opens a pool and applies pending migrations.
func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	if err := Migrate(pool); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

func Migrate(pool *pgxpool.Pool) error {
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("migrations source: %w", err)
	}
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	driver, err := pgxmigrate.WithInstance(db, &pgxmigrate.Config{})
	if err != nil {
		return fmt.Errorf("migrations driver: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", src, "pgx5", driver)
	if err != nil {
		return fmt.Errorf("migrations: %w", err)
	}
	// Close releases the connection the driver holds; without it pool.Close blocks forever.
	defer m.Close()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate up: %w", err)
	}
	return nil
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	return err
}

// --- Guilds ---

type GuildRepo struct{ db *pgxpool.Pool }

func NewGuildRepo(db *pgxpool.Pool) *GuildRepo { return &GuildRepo{db: db} }

const guildCols = `discord_id, name, admin_discord_id, anonymous, timezone, survey_hour,
	report_frequency, language, kudos_channel_id, games_enabled, working_days, survey_message, active, created_at, updated_at`

func scanGuild(row pgx.Row) (domain.Guild, error) {
	var g domain.Guild
	var freq string
	var workingDays int16
	err := row.Scan(&g.DiscordID, &g.Name, &g.AdminDiscordID, &g.Settings.Anonymous,
		&g.Settings.Timezone, &g.Settings.SurveyHour, &freq, &g.Settings.Language, &g.Settings.KudosChannelID, &g.Settings.GamesEnabled, &workingDays, &g.Settings.SurveyMessage, &g.Active, &g.CreatedAt, &g.UpdatedAt)
	g.Settings.ReportFrequency = domain.ReportFrequency(freq)
	g.Settings.WorkingDays = domain.Weekdays(workingDays)
	return g, notFound(err)
}

func (r *GuildRepo) Upsert(ctx context.Context, g domain.Guild) (domain.Guild, error) {
	s := g.Settings
	return scanGuild(r.db.QueryRow(ctx, `
		INSERT INTO guilds (discord_id, name, admin_discord_id, anonymous, timezone, survey_hour, report_frequency, language, working_days)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (discord_id) DO UPDATE
		SET name = EXCLUDED.name,
		    active = TRUE, left_at = NULL, updated_at = now()
		RETURNING `+guildCols,
		g.DiscordID, g.Name, g.AdminDiscordID, s.Anonymous, s.Timezone, s.SurveyHour, string(s.ReportFrequency), s.Language, int16(s.WorkingDays)))
}

func (r *GuildRepo) Get(ctx context.Context, guildID string) (domain.Guild, error) {
	return scanGuild(r.db.QueryRow(ctx, `SELECT `+guildCols+` FROM guilds WHERE discord_id = $1`, guildID))
}

func (r *GuildRepo) ListActive(ctx context.Context) ([]domain.Guild, error) {
	return r.list(ctx, `SELECT `+guildCols+` FROM guilds WHERE active ORDER BY id`)
}

func (r *GuildRepo) ListVisible(ctx context.Context, userID string, guildIDs []string) ([]domain.Guild, error) {
	if guildIDs == nil {
		guildIDs = []string{}
	}
	return r.list(ctx, `SELECT `+guildCols+` FROM guilds g
		WHERE active AND (admin_discord_id = $1 OR discord_id = ANY($2) OR EXISTS (
		    SELECT 1 FROM members m JOIN access_grants a ON a.guild_id = m.guild_id
		    WHERE m.guild_id = g.id AND m.discord_id = $1 AND m.in_server
		      AND ((a.kind = 'user' AND a.target_id = $1) OR (a.kind = 'role' AND a.target_id = ANY(m.role_ids)))
		)) ORDER BY name, id`, userID, guildIDs)
}

func (r *GuildRepo) ListJoined(ctx context.Context, userID string) ([]domain.Guild, error) {
	return r.list(ctx, `SELECT `+guildCols+` FROM guilds
		WHERE active AND id IN (SELECT guild_id FROM members WHERE discord_id = $1 AND active)
		ORDER BY name, id`, userID)
}

func (r *GuildRepo) list(ctx context.Context, sql string, args ...any) ([]domain.Guild, error) {
	rows, err := r.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Guild
	for rows.Next() {
		g, err := scanGuild(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (r *GuildRepo) UpdateConfig(ctx context.Context, guildID, adminID string, s domain.GuildSettings) (domain.Guild, error) {
	return scanGuild(r.db.QueryRow(ctx, `
		UPDATE guilds SET admin_discord_id = $2, anonymous = $3, timezone = $4, survey_hour = $5,
		       report_frequency = $6, language = $7, kudos_channel_id = $8, games_enabled = $9,
		       working_days = $10, survey_message = $11, updated_at = now()
		WHERE discord_id = $1
		RETURNING `+guildCols,
		guildID, adminID, s.Anonymous, s.Timezone, s.SurveyHour, string(s.ReportFrequency), s.Language, s.KudosChannelID, s.GamesEnabled,
		int16(s.WorkingDays), s.SurveyMessage))
}

func (r *GuildRepo) Deactivate(ctx context.Context, guildID string, at time.Time) error {
	tag, err := r.db.Exec(ctx, `
		UPDATE guilds SET active = FALSE, left_at = COALESCE(left_at, $2), updated_at = now()
		WHERE discord_id = $1`, guildID, at)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *GuildRepo) Delete(ctx context.Context, guildID string) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }() // a no-op after Commit
	// Members, moods, kudos, games and dispatches go with the guild (ON DELETE CASCADE).
	tag, err := tx.Exec(ctx, `DELETE FROM guilds WHERE discord_id = $1`, guildID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	if _, err := tx.Exec(ctx, `INSERT INTO guild_removals (discord_id) VALUES ($1) ON CONFLICT DO NOTHING`, guildID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *GuildRepo) DeleteLeftBefore(ctx context.Context, before time.Time) ([]string, error) {
	rows, err := r.db.Query(ctx, `DELETE FROM guilds WHERE NOT active AND left_at < $1 RETURNING discord_id`, before)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func (r *GuildRepo) ListRemovals(ctx context.Context) ([]string, error) {
	rows, err := r.db.Query(ctx, `SELECT discord_id FROM guild_removals ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func (r *GuildRepo) Removing(ctx context.Context, guildID string) (bool, error) {
	var ok bool
	err := r.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM guild_removals WHERE discord_id = $1)`, guildID).Scan(&ok)
	return ok, err
}

func (r *GuildRepo) DoneRemoving(ctx context.Context, guildID string) error {
	_, err := r.db.Exec(ctx, `DELETE FROM guild_removals WHERE discord_id = $1`, guildID)
	return err
}

// --- Members ---

type MemberRepo struct{ db *pgxpool.Pool }

func NewMemberRepo(db *pgxpool.Pool) *MemberRepo { return &MemberRepo{db: db} }

const memberCols = `g.discord_id, m.discord_id, m.username, m.active, m.role_ids, m.in_server, m.created_at`

func scanMember(row pgx.Row) (domain.Member, error) {
	var m domain.Member
	err := row.Scan(&m.GuildDiscordID, &m.DiscordID, &m.Username, &m.Active, &m.RoleIDs, &m.InServer, &m.CreatedAt)
	return m, notFound(err)
}

func (r *MemberRepo) Upsert(ctx context.Context, m domain.Member, setActive *bool) (domain.Member, error) {
	return scanMember(r.db.QueryRow(ctx, `
		WITH g AS (SELECT id, discord_id FROM guilds WHERE discord_id = $1),
		     m AS (
		         INSERT INTO members (guild_id, discord_id, username, active, role_ids)
		         SELECT g.id, $2, $3, COALESCE($4::boolean, TRUE), COALESCE($5::text[], '{}') FROM g
		         ON CONFLICT (guild_id, discord_id) DO UPDATE
		         SET username = EXCLUDED.username,
		             active = COALESCE($4::boolean, members.active),
		             role_ids = COALESCE($5::text[], members.role_ids),
		             in_server = TRUE,
		             updated_at = now()
		         RETURNING *
		     )
		SELECT `+memberCols+` FROM m JOIN g ON g.id = m.guild_id`,
		m.GuildDiscordID, m.DiscordID, m.Username, setActive, m.RoleIDs))
}

func (r *MemberRepo) SyncMany(ctx context.Context, guildID string, ms []domain.Member) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }() // a no-op after Commit
	var gid int64
	if err := tx.QueryRow(ctx, `SELECT id FROM guilds WHERE discord_id = $1`, guildID).Scan(&gid); err != nil {
		return notFound(err)
	}
	b := &pgx.Batch{}
	for _, m := range ms {
		b.Queue(`
			INSERT INTO members (guild_id, discord_id, username, role_ids) VALUES ($1, $2, $3, $4)
			ON CONFLICT (guild_id, discord_id) DO UPDATE
			SET username = EXCLUDED.username, role_ids = EXCLUDED.role_ids, in_server = TRUE, updated_at = now()`,
			gid, m.DiscordID, m.Username, m.RoleIDs)
	}
	if err := tx.SendBatch(ctx, b).Close(); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *MemberRepo) Get(ctx context.Context, guildID, userID string) (domain.Member, error) {
	return scanMember(r.db.QueryRow(ctx, `
		SELECT `+memberCols+` FROM members m JOIN guilds g ON g.id = m.guild_id
		WHERE g.discord_id = $1 AND m.discord_id = $2`, guildID, userID))
}

func (r *MemberRepo) ListActive(ctx context.Context, guildID string) ([]domain.Member, error) {
	return r.list(ctx, `
		SELECT `+memberCols+` FROM members m JOIN guilds g ON g.id = m.guild_id
		WHERE g.discord_id = $1 AND m.active ORDER BY m.id`, guildID)
}

func (r *MemberRepo) ListInServer(ctx context.Context, guildID string) ([]domain.Member, error) {
	return r.list(ctx, `
		SELECT `+memberCols+` FROM members m JOIN guilds g ON g.id = m.guild_id
		WHERE g.discord_id = $1 AND m.in_server ORDER BY lower(m.username), m.id`, guildID)
}

func (r *MemberRepo) list(ctx context.Context, sql string, args ...any) ([]domain.Member, error) {
	rows, err := r.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Member
	for rows.Next() {
		m, err := scanMember(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (r *MemberRepo) Deactivate(ctx context.Context, guildID, userID string) error {
	return r.deactivate(ctx, guildID, userID, false)
}

func (r *MemberRepo) Left(ctx context.Context, guildID, userID string) error {
	return r.deactivate(ctx, guildID, userID, true)
}

func (r *MemberRepo) deactivate(ctx context.Context, guildID, userID string, left bool) error {
	tag, err := r.db.Exec(ctx, `
		UPDATE members m SET active = FALSE, in_server = in_server AND NOT $3, updated_at = now()
		FROM guilds g
		WHERE g.id = m.guild_id AND g.discord_id = $1 AND m.discord_id = $2`, guildID, userID, left)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// --- Moods ---

type MoodRepo struct{ db *pgxpool.Pool }

func NewMoodRepo(db *pgxpool.Pool) *MoodRepo { return &MoodRepo{db: db} }

const moodCols = `g.discord_id, m.discord_id, e.day, e.mood, e.message, e.anonymous, e.created_at, e.updated_at`

func scanMood(row pgx.Row) (domain.MoodEntry, error) {
	var e domain.MoodEntry
	var mood int16
	err := row.Scan(&e.GuildDiscordID, &e.MemberDiscordID, &e.Day, &mood, &e.Message, &e.Anonymous, &e.CreatedAt, &e.UpdatedAt)
	e.Mood = domain.Mood(mood)
	e.Day = domain.TruncateDay(e.Day)
	return e, notFound(err)
}

func (r *MoodRepo) Upsert(ctx context.Context, e domain.MoodEntry) (domain.MoodEntry, error) {
	// The member lookup is scoped to the guild, so an entry can never be attached to
	// another guild's member. The entry takes the guild's anonymity at the moment it is
	// written: answering again after the guild went public makes that answer public.
	return scanMood(r.db.QueryRow(ctx, `
		WITH t AS (
		         SELECT g.id AS guild_id, g.discord_id AS guild_discord_id, g.anonymous, m.id AS member_id, m.discord_id AS member_discord_id
		         FROM members m JOIN guilds g ON g.id = m.guild_id
		         WHERE g.discord_id = $1 AND m.discord_id = $2
		     ),
		     e AS (
		         INSERT INTO mood_entries (guild_id, member_id, day, mood, message, anonymous)
		         SELECT t.guild_id, t.member_id, $3, $4, $5, t.anonymous FROM t
		         ON CONFLICT (member_id, day) DO UPDATE
		         SET mood = EXCLUDED.mood, message = EXCLUDED.message, anonymous = EXCLUDED.anonymous, updated_at = now()
		         RETURNING *
		     )
		SELECT t.guild_discord_id, t.member_discord_id, e.day, e.mood, e.message, e.anonymous, e.created_at, e.updated_at
		FROM e JOIN t ON t.member_id = e.member_id`,
		e.GuildDiscordID, e.MemberDiscordID, e.Day, int16(e.Mood), e.Message))
}

func (r *MoodRepo) ListBetween(ctx context.Context, guildID string, from, to time.Time) ([]domain.MoodEntry, error) {
	return r.list(ctx, `
		SELECT `+moodCols+`
		FROM mood_entries e
		JOIN guilds g ON g.id = e.guild_id
		JOIN members m ON m.id = e.member_id
		WHERE g.discord_id = $1 AND e.day BETWEEN $2 AND $3
		ORDER BY e.day, e.id`, guildID, from, to)
}

// Streaks counts, for each active member of the guild, how many days in a row up to today they
// answered, like domain.Streak but in the database, so a big guild sends back one number per
// member instead of every answer. Answers are read from `from`.
//
// A streak ends at the member's latest missed day: the latest working day before today they
// didn't answer (today itself can still be answered). The streak is then the answers after it.
// Both steps walk the (member_id, day) index from today backwards, so the work grows with the
// streaks, not with the guild's history.
func (r *MoodRepo) Streaks(ctx context.Context, guildID string, workingDays domain.Weekdays, from, today time.Time) (map[string]int, error) {
	rows, err := r.db.Query(ctx, `
		SELECT m.discord_id, s.n
		FROM members m
		JOIN guilds g ON g.id = m.guild_id
		CROSS JOIN LATERAL (
			SELECT COALESCE((
				SELECT d::date FROM generate_series($3::date - 1, $2::date, interval '-1 day') AS d
				WHERE ($4::int >> EXTRACT(DOW FROM d)::int) & 1 = 1
				  AND NOT EXISTS (SELECT 1 FROM mood_entries e WHERE e.member_id = m.id AND e.day = d::date)
				ORDER BY d DESC LIMIT 1
			), $2::date - 1) AS missed
		) x
		CROSS JOIN LATERAL (
			SELECT count(*) AS n FROM mood_entries e
			WHERE e.member_id = m.id AND e.day > x.missed AND e.day <= $3::date
		) s
		WHERE g.discord_id = $1 AND m.active AND s.n > 0`,
		guildID, from, today, int(workingDays))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

func (r *MoodRepo) ListForUser(ctx context.Context, userID string, from, to time.Time) ([]domain.MoodEntry, error) {
	return r.list(ctx, `
		SELECT `+moodCols+`
		FROM mood_entries e
		JOIN guilds g ON g.id = e.guild_id
		JOIN members m ON m.id = e.member_id
		WHERE m.discord_id = $1 AND e.day BETWEEN $2 AND $3
		ORDER BY e.day, e.id`, userID, from, to)
}

func (r *MoodRepo) list(ctx context.Context, sql string, args ...any) ([]domain.MoodEntry, error) {
	rows, err := r.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.MoodEntry
	for rows.Next() {
		e, err := scanMood(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// --- Kudos ---

type KudosRepo struct{ db *pgxpool.Pool }

func NewKudosRepo(db *pgxpool.Pool) *KudosRepo { return &KudosRepo{db: db} }

// Both members are looked up within the guild, so kudos can never link members of
// different guilds.
const kudosCols = `g.discord_id, gm.discord_id, gm.username, rm.discord_id, rm.username, k.day, k.message, k.created_at`

func scanKudos(row pgx.Row) (domain.Kudos, error) {
	var k domain.Kudos
	err := row.Scan(&k.GuildDiscordID, &k.GiverDiscordID, &k.GiverUsername, &k.ReceiverDiscordID,
		&k.ReceiverUsername, &k.Day, &k.Message, &k.CreatedAt)
	k.Day = domain.TruncateDay(k.Day)
	return k, notFound(err)
}

func (r *KudosRepo) Create(ctx context.Context, k domain.Kudos) (domain.Kudos, error) {
	return scanKudos(r.db.QueryRow(ctx, `
		WITH k AS (
		         INSERT INTO kudos (guild_id, giver_member_id, receiver_member_id, day, message)
		         SELECT g.id, gm.id, rm.id, $4, $5
		         FROM guilds g
		         JOIN members gm ON gm.guild_id = g.id AND gm.discord_id = $2
		         JOIN members rm ON rm.guild_id = g.id AND rm.discord_id = $3
		         WHERE g.discord_id = $1
		         RETURNING *
		     )
		SELECT `+kudosCols+`
		FROM k
		JOIN guilds g ON g.id = k.guild_id
		JOIN members gm ON gm.id = k.giver_member_id
		JOIN members rm ON rm.id = k.receiver_member_id`,
		k.GuildDiscordID, k.GiverDiscordID, k.ReceiverDiscordID, k.Day, k.Message))
}

func (r *KudosRepo) CountGivenOn(ctx context.Context, guildID, giverID string, day time.Time) (int, error) {
	var n int
	err := r.db.QueryRow(ctx, `
		SELECT count(*) FROM kudos k
		JOIN guilds g ON g.id = k.guild_id
		JOIN members gm ON gm.id = k.giver_member_id
		WHERE g.discord_id = $1 AND gm.discord_id = $2 AND k.day = $3`, guildID, giverID, day).Scan(&n)
	return n, err
}

func (r *KudosRepo) ListBetween(ctx context.Context, guildID string, from, to time.Time) ([]domain.Kudos, error) {
	rows, err := r.db.Query(ctx, `
		SELECT `+kudosCols+`
		FROM kudos k
		JOIN guilds g ON g.id = k.guild_id
		JOIN members gm ON gm.id = k.giver_member_id
		JOIN members rm ON rm.id = k.receiver_member_id
		WHERE g.discord_id = $1 AND k.day BETWEEN $2 AND $3
		ORDER BY k.created_at, k.id`, guildID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Kudos
	for rows.Next() {
		k, err := scanKudos(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// --- Games ---

type GameRepo struct{ db *pgxpool.Pool }

func NewGameRepo(db *pgxpool.Pool) *GameRepo { return &GameRepo{db: db} }

// Every game query reaches the round through its guild's Discord ID, and members through that
// same guild, so answers and guesses can never cross guilds.

func (r *GameRepo) CreateRound(ctx context.Context, rd domain.GameRound) (domain.GameRound, error) {
	_, err := r.db.Exec(ctx, `
		INSERT INTO game_rounds (guild_id, week, question, proposer_member_id)
		SELECT g.id, $2, $3, (SELECT m.id FROM members m WHERE m.guild_id = g.id AND m.discord_id = NULLIF($4, ''))
		FROM guilds g WHERE g.discord_id = $1
		ON CONFLICT (guild_id, week) DO NOTHING`, rd.GuildDiscordID, rd.Week, rd.Question, rd.ProposerDiscordID)
	if err != nil {
		return domain.GameRound{}, err
	}
	return r.GetRound(ctx, rd.GuildDiscordID, rd.Week)
}

// gameRoundCols needs members m LEFT JOINed on the proposer.
const gameRoundCols = `g.discord_id, rd.week, rd.question, COALESCE(m.discord_id, ''), COALESCE(m.username, ''),
	rd.proposer_member_id IS NOT NULL, rd.created_at`

func scanGameRound(row pgx.Row) (domain.GameRound, error) {
	var rd domain.GameRound
	err := row.Scan(&rd.GuildDiscordID, &rd.Week, &rd.Question, &rd.ProposerDiscordID, &rd.ProposerUsername, &rd.Proposed, &rd.CreatedAt)
	rd.Week = domain.TruncateDay(rd.Week)
	return rd, notFound(err)
}

func (r *GameRepo) GetRound(ctx context.Context, guildID string, week time.Time) (domain.GameRound, error) {
	return scanGameRound(r.db.QueryRow(ctx, `
		SELECT `+gameRoundCols+`
		FROM game_rounds rd JOIN guilds g ON g.id = rd.guild_id
		LEFT JOIN members m ON m.id = rd.proposer_member_id
		WHERE g.discord_id = $1 AND rd.week = $2`, guildID, week))
}

const gameAnswerCols = `a.ref::text, g.discord_id, rd.week, m.discord_id, m.username, a.answer, a.created_at, a.updated_at`

func scanGameAnswer(row pgx.Row) (domain.GameAnswer, error) {
	var a domain.GameAnswer
	err := row.Scan(&a.ID, &a.GuildDiscordID, &a.Week, &a.MemberDiscordID, &a.Username, &a.Text, &a.CreatedAt, &a.UpdatedAt)
	a.Week = domain.TruncateDay(a.Week)
	return a, notFound(err)
}

func (r *GameRepo) UpsertAnswer(ctx context.Context, a domain.GameAnswer) (domain.GameAnswer, error) {
	return scanGameAnswer(r.db.QueryRow(ctx, `
		WITH t AS (
		         SELECT rd.id AS round_id, m.id AS member_id
		         FROM game_rounds rd
		         JOIN guilds g ON g.id = rd.guild_id
		         JOIN members m ON m.guild_id = g.id AND m.discord_id = $3
		         WHERE g.discord_id = $1 AND rd.week = $2
		     ),
		     a AS (
		         INSERT INTO game_answers (round_id, member_id, answer)
		         SELECT round_id, member_id, $4 FROM t
		         ON CONFLICT (round_id, member_id) DO UPDATE
		         SET answer = EXCLUDED.answer, updated_at = now()
		         RETURNING *
		     )
		SELECT `+gameAnswerCols+`
		FROM a
		JOIN game_rounds rd ON rd.id = a.round_id
		JOIN guilds g ON g.id = rd.guild_id
		JOIN members m ON m.id = a.member_id`,
		a.GuildDiscordID, a.Week, a.MemberDiscordID, a.Text))
}

func (r *GameRepo) ListAnswers(ctx context.Context, guildID string, week time.Time) ([]domain.GameAnswer, error) {
	rows, err := r.db.Query(ctx, `
		SELECT `+gameAnswerCols+`
		FROM game_answers a
		JOIN game_rounds rd ON rd.id = a.round_id
		JOIN guilds g ON g.id = rd.guild_id
		JOIN members m ON m.id = a.member_id AND m.guild_id = g.id
		WHERE g.discord_id = $1 AND rd.week = $2 AND m.active
		ORDER BY a.ref`, guildID, week)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.GameAnswer
	for rows.Next() {
		a, err := scanGameAnswer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *GameRepo) ReplaceGuesses(ctx context.Context, guildID string, week time.Time, guesserID string, guesses []domain.GameGuess) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }() // a no-op after Commit
	var roundID, guildPK, guesserPK int64
	err = tx.QueryRow(ctx, `
		SELECT rd.id, g.id, m.id
		FROM game_rounds rd
		JOIN guilds g ON g.id = rd.guild_id
		JOIN members m ON m.guild_id = g.id AND m.discord_id = $3
		WHERE g.discord_id = $1 AND rd.week = $2`, guildID, week, guesserID).Scan(&roundID, &guildPK, &guesserPK)
	if err != nil {
		return notFound(err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM game_guesses WHERE guesser_member_id = $1
		AND answer_id IN (SELECT id FROM game_answers WHERE round_id = $2)`, guesserPK, roundID); err != nil {
		return err
	}
	for _, gs := range guesses {
		tag, err := tx.Exec(ctx, `
			INSERT INTO game_guesses (answer_id, guesser_member_id, guessed_member_id)
			SELECT a.id, $3, m.id
			FROM game_answers a
			JOIN members m ON m.guild_id = $4 AND m.discord_id = $5
			WHERE a.round_id = $1 AND a.ref::text = $2`, roundID, gs.AnswerID, guesserPK, guildPK, gs.GuessedDiscordID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return domain.ErrNotFound
		}
	}
	return tx.Commit(ctx)
}

func (r *GameRepo) ListGuesses(ctx context.Context, guildID string, week time.Time) ([]domain.GameGuess, error) {
	rows, err := r.db.Query(ctx, `
		SELECT gm.discord_id, a.ref::text, tm.discord_id
		FROM game_guesses gs
		JOIN game_answers a ON a.id = gs.answer_id
		JOIN game_rounds rd ON rd.id = a.round_id
		JOIN guilds g ON g.id = rd.guild_id
		JOIN members gm ON gm.id = gs.guesser_member_id AND gm.guild_id = g.id
		JOIN members tm ON tm.id = gs.guessed_member_id AND tm.guild_id = g.id
		WHERE g.discord_id = $1 AND rd.week = $2 AND gm.active
		ORDER BY gs.created_at, a.id`, guildID, week)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.GameGuess
	for rows.Next() {
		var gs domain.GameGuess
		if err := rows.Scan(&gs.GuesserDiscordID, &gs.AnswerID, &gs.GuessedDiscordID); err != nil {
			return nil, err
		}
		out = append(out, gs)
	}
	return out, rows.Err()
}

const gameProposalCols = `g.discord_id, p.week, m.discord_id, m.username, p.question`

func scanGameProposal(row pgx.Row) (domain.GameProposal, error) {
	var p domain.GameProposal
	err := row.Scan(&p.GuildDiscordID, &p.Week, &p.MemberDiscordID, &p.Username, &p.Text)
	p.Week = domain.TruncateDay(p.Week)
	return p, notFound(err)
}

func (r *GameRepo) UpsertProposal(ctx context.Context, p domain.GameProposal) (domain.GameProposal, error) {
	return scanGameProposal(r.db.QueryRow(ctx, `
		WITH p AS (
		         INSERT INTO game_proposals (guild_id, week, member_id, question)
		         SELECT g.id, $2, m.id, $4
		         FROM guilds g JOIN members m ON m.guild_id = g.id AND m.discord_id = $3
		         WHERE g.discord_id = $1
		         ON CONFLICT (guild_id, week, member_id) DO UPDATE
		         SET question = EXCLUDED.question, updated_at = now()
		         RETURNING *
		     )
		SELECT `+gameProposalCols+`
		FROM p JOIN guilds g ON g.id = p.guild_id JOIN members m ON m.id = p.member_id`,
		p.GuildDiscordID, p.Week, p.MemberDiscordID, p.Text))
}

func (r *GameRepo) ListProposals(ctx context.Context, guildID string, week time.Time) ([]domain.GameProposal, error) {
	rows, err := r.db.Query(ctx, `
		SELECT `+gameProposalCols+`
		FROM game_proposals p
		JOIN guilds g ON g.id = p.guild_id
		JOIN members m ON m.id = p.member_id AND m.guild_id = g.id
		WHERE g.discord_id = $1 AND p.week = $2 AND m.active
		ORDER BY p.id`, guildID, week)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.GameProposal
	for rows.Next() {
		p, err := scanGameProposal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// --- Dispatches ---

type DispatchRepo struct{ db *pgxpool.Pool }

func NewDispatchRepo(db *pgxpool.Pool) *DispatchRepo { return &DispatchRepo{db: db} }

func (r *DispatchRepo) Claim(ctx context.Context, guildID string, kind domain.DispatchKind, period time.Time) (bool, error) {
	tag, err := r.db.Exec(ctx, `
		INSERT INTO dispatches (guild_id, kind, period)
		SELECT id, $2, $3 FROM guilds WHERE discord_id = $1
		ON CONFLICT DO NOTHING`, guildID, string(kind), period)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}
