package http

import (
	"net/http"
	"strconv"
	"time"

	"github.com/moixblau/HappyX.Api/internal/domain"
	"github.com/moixblau/HappyX.Api/internal/usecase"
)

const dateLayout = "2006-01-02"

// --- DTOs ---

type guildDTO struct {
	DiscordID       string    `json:"discord_guild_id"`
	Name            string    `json:"name"`
	AdminDiscordID  string    `json:"admin_discord_user_id"`
	Anonymous       bool      `json:"anonymous"`
	Timezone        string    `json:"timezone"`
	Language        string    `json:"language"`
	SurveyHour      int       `json:"survey_hour"`
	ReportFrequency string    `json:"report_frequency"`
	KudosChannelID  string    `json:"kudos_channel_id"`
	GamesEnabled    bool      `json:"games_enabled"`
	WorkingDays     []int     `json:"working_days"`   // ISO days, 1 is Monday and 7 Sunday
	SurveyMessage   string    `json:"survey_message"` // "" means the default text
	Active          bool      `json:"active"`
	CreatedAt       time.Time `json:"created_at"`
}

func toGuildDTO(g domain.Guild) guildDTO {
	return guildDTO{
		DiscordID: g.DiscordID, Name: g.Name, AdminDiscordID: g.AdminDiscordID,
		Anonymous: g.Settings.Anonymous, Timezone: g.Settings.Timezone, Language: g.Settings.Language,
		SurveyHour: g.Settings.SurveyHour, ReportFrequency: string(g.Settings.ReportFrequency),
		KudosChannelID: g.Settings.KudosChannelID, GamesEnabled: g.Settings.GamesEnabled,
		WorkingDays: g.Settings.WorkingDays.ISO(), SurveyMessage: g.Settings.SurveyMessage, Active: g.Active, CreatedAt: g.CreatedAt,
	}
}

type roleDTO struct {
	DiscordID string `json:"id"`
	Name      string `json:"name"`
	Color     int    `json:"color"`
	Position  int    `json:"position"`
}

type memberDTO struct {
	DiscordID string    `json:"discord_user_id"`
	Username  string    `json:"username"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
	Streak    int       `json:"streak,omitempty"` // only with ?with=streak
}

type moodDTO struct {
	DiscordID string `json:"discord_user_id"`
	Day       string `json:"day"`
	Mood      int    `json:"mood"`
	Message   string `json:"message,omitempty"`
}

type reportDTO struct {
	DiscordGuildID string          `json:"discord_guild_id"`
	From           string          `json:"from"`
	To             string          `json:"to"`
	Anonymous      bool            `json:"anonymous"`
	Responses      int             `json:"responses"`
	Respondents    int             `json:"respondents"`
	Average        float64         `json:"average"`
	Distribution   map[string]int  `json:"distribution"`
	Days           []dayDTO        `json:"days"`
	Messages       []reportMsgDTO  `json:"messages"`
	Kudos          kudosSummaryDTO `json:"kudos"`
}

type kudosSummaryDTO struct {
	Count      int        `json:"count"`
	Givers     int        `json:"givers"`
	Receivers  int        `json:"receivers"`
	Highlights []kudosDTO `json:"highlights"`
}

type kudosDTO struct {
	Day          string    `json:"day"`
	FromID       string    `json:"from_discord_user_id"`
	FromUsername string    `json:"from_username"`
	ToID         string    `json:"to_discord_user_id"`
	ToUsername   string    `json:"to_username"`
	Message      string    `json:"message"`
	CreatedAt    time.Time `json:"created_at"`
}

func toKudosDTO(k domain.Kudos) kudosDTO {
	return kudosDTO{
		Day: k.Day.Format(dateLayout), FromID: k.GiverDiscordID, FromUsername: k.GiverUsername,
		ToID: k.ReceiverDiscordID, ToUsername: k.ReceiverUsername, Message: k.Message, CreatedAt: k.CreatedAt,
	}
}

type dayDTO struct {
	Day       string  `json:"day"`
	Responses int     `json:"responses"`
	Average   float64 `json:"average"`
}

type reportMsgDTO struct {
	Day       string `json:"day"`
	Mood      int    `json:"mood"`
	Message   string `json:"message"`
	DiscordID string `json:"discord_user_id,omitempty"`
	Username  string `json:"username,omitempty"`
}

// --- Guilds ---

func (s *Server) listGuilds(w http.ResponseWriter, r *http.Request) {
	gs, err := s.svc.ListActiveGuilds(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]guildDTO, 0, len(gs))
	for _, g := range gs {
		out = append(out, toGuildDTO(g))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) registerGuild(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DiscordID      string `json:"discord_guild_id"`
		Name           string `json:"name"`
		AdminDiscordID string `json:"admin_discord_user_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	g, err := s.svc.RegisterGuild(r.Context(), usecase.RegisterGuildInput{
		DiscordID: in.DiscordID, Name: in.Name, AdminDiscordID: in.AdminDiscordID,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toGuildDTO(g))
}

func (s *Server) getGuild(w http.ResponseWriter, r *http.Request) {
	g, err := s.svc.GetGuild(r.Context(), r.PathValue("guildID"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toGuildDTO(g))
}

func (s *Server) updateGuild(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Anonymous       *bool   `json:"anonymous"`
		Timezone        *string `json:"timezone"`
		Language        *string `json:"language"`
		SurveyHour      *int    `json:"survey_hour"`
		ReportFrequency *string `json:"report_frequency"`
		AdminDiscordID  *string `json:"admin_discord_user_id"`
		KudosChannelID  *string `json:"kudos_channel_id"` // "" clears it
		GamesEnabled    *bool   `json:"games_enabled"`
		WorkingDays     *[]int  `json:"working_days"`
	}
	if !decode(w, r, &in) {
		return
	}
	p := usecase.SettingsPatch{
		Anonymous: in.Anonymous, Timezone: in.Timezone, Language: in.Language, SurveyHour: in.SurveyHour,
		AdminDiscordID: in.AdminDiscordID, KudosChannelID: in.KudosChannelID, GamesEnabled: in.GamesEnabled,
	}
	if in.ReportFrequency != nil {
		f := domain.ReportFrequency(*in.ReportFrequency)
		p.ReportFrequency = &f
	}
	if !s.patchWorkingDays(w, r, in.WorkingDays, &p) {
		return
	}
	g, err := s.svc.UpdateGuildSettings(r.Context(), r.PathValue("guildID"), p)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toGuildDTO(g))
}

func (s *Server) removeGuild(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.RemoveGuild(r.Context(), r.PathValue("guildID")); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// listRemovals returns the Discord IDs of the servers deleted from HappyX that the bot must leave.
func (s *Server) listRemovals(w http.ResponseWriter, r *http.Request) {
	ids, err := s.svc.GuildRemovals(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if ids == nil {
		ids = []string{}
	}
	writeJSON(w, http.StatusOK, ids)
}

func (s *Server) doneRemoving(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.GuildRemoved(r.Context(), r.PathValue("guildID")); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Members ---

func (s *Server) listMembers(w http.ResponseWriter, r *http.Request) {
	ms, err := s.svc.ListMembers(r.Context(), r.PathValue("guildID"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// ?with=streak adds each member's answering streak, for a custom survey message that uses it.
	var streaks map[string]int
	if r.URL.Query().Get("with") == "streak" {
		if streaks, err = s.svc.MemberStreaks(r.Context(), r.PathValue("guildID")); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	out := make([]memberDTO, 0, len(ms))
	for _, m := range ms {
		out = append(out, memberDTO{DiscordID: m.DiscordID, Username: m.Username, Active: m.Active, CreatedAt: m.CreatedAt, Streak: streaks[m.DiscordID]})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) joinMember(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		// Active is optional: omitted keeps an existing member's opt-in state.
		Active *bool `json:"active"`
		// RoleIDs is optional: omitted keeps the member's stored Discord roles.
		RoleIDs *[]string `json:"role_ids"`
	}
	if !decode(w, r, &in) {
		return
	}
	var roles []string
	if in.RoleIDs != nil {
		roles = append([]string{}, *in.RoleIDs...)
	}
	m, err := s.svc.UpsertMember(r.Context(), r.PathValue("guildID"), r.PathValue("userID"), in.Username, in.Active, roles)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, memberDTO{DiscordID: m.DiscordID, Username: m.Username, Active: m.Active, CreatedAt: m.CreatedAt})
}

// syncMembersMaxBytes fits a full page of members with many roles each.
const syncMembersMaxBytes = 8 << 20

// syncMembers registers up to 1000 members or refreshes their usernames and roles, keeping
// everyone's opt-in state. The bot sends its member list this way when it connects.
func (s *Server) syncMembers(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Members []struct {
			DiscordID string   `json:"discord_user_id"`
			Username  string   `json:"username"`
			RoleIDs   []string `json:"role_ids"`
		} `json:"members"`
	}
	if !decodeLimit(w, r, &in, syncMembersMaxBytes) {
		return
	}
	ms := make([]domain.Member, 0, len(in.Members))
	for _, m := range in.Members {
		ms = append(ms, domain.Member{DiscordID: m.DiscordID, Username: m.Username, RoleIDs: m.RoleIDs})
	}
	if err := s.svc.SyncMembers(r.Context(), r.PathValue("guildID"), ms); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// leaveMember stops the survey for the member: they paused it or, with ?left=true, they left
// the server.
func (s *Server) leaveMember(w http.ResponseWriter, r *http.Request) {
	left := r.URL.Query().Get("left") == "true"
	if err := s.svc.LeaveMember(r.Context(), r.PathValue("guildID"), r.PathValue("userID"), left); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// memberAccess answers {"level": "none"|"view"|"manage"|"owner"} so the bot can check its admin
// commands. ?discord_admin=true says Discord reports them as the owner or an Administrator.
func (s *Server) memberAccess(w http.ResponseWriter, r *http.Request) {
	level, err := s.svc.MemberAccess(r.Context(), r.PathValue("guildID"), r.PathValue("userID"),
		r.URL.Query().Get("discord_admin") == "true")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"level": level.String()})
}

// syncRoles replaces the guild's Discord roles with {"roles": [...]}.
func (s *Server) syncRoles(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Roles []roleDTO `json:"roles"`
	}
	if !decode(w, r, &in) {
		return
	}
	roles := make([]domain.Role, 0, len(in.Roles))
	for _, ro := range in.Roles {
		roles = append(roles, domain.Role{DiscordID: ro.DiscordID, Name: ro.Name, Color: ro.Color, Position: ro.Position})
	}
	if err := s.svc.SyncRoles(r.Context(), r.PathValue("guildID"), roles); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Moods ---

func (s *Server) recordMood(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Mood    int    `json:"mood"`
		Message string `json:"message"`
		Day     string `json:"day"` // optional, YYYY-MM-DD; defaults to today in the guild's timezone
	}
	if !decode(w, r, &in) {
		return
	}
	var day time.Time
	if in.Day != "" {
		var err error
		if day, err = time.Parse(dateLayout, in.Day); err != nil {
			writeError(w, http.StatusBadRequest, "day must be YYYY-MM-DD")
			return
		}
	}
	e, err := s.svc.RecordMood(r.Context(), usecase.RecordMoodInput{
		GuildID: r.PathValue("guildID"), UserID: r.PathValue("userID"),
		Mood: domain.Mood(in.Mood), Message: in.Message, Day: day,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, moodDTO{
		DiscordID: e.MemberDiscordID, Day: e.Day.Format(dateLayout), Mood: int(e.Mood), Message: e.Message,
	})
}

// --- Reports ---

// report returns aggregated data for ?from=YYYY-MM-DD&to=YYYY-MM-DD. Without a range it
// returns the last complete period (week or month) according to the guild's settings.
func (s *Server) report(w http.ResponseWriter, r *http.Request) {
	g, err := s.svc.GetGuild(r.Context(), r.PathValue("guildID"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeReport(w, r, g)
}

func (s *Server) writeReport(w http.ResponseWriter, r *http.Request, g domain.Guild) {
	q := r.URL.Query()
	var from, to time.Time
	if q.Get("from") == "" && q.Get("to") == "" {
		from, to = s.svc.LastPeriod(g)
	} else {
		var err1, err2 error
		from, err1 = time.Parse(dateLayout, q.Get("from"))
		to, err2 = time.Parse(dateLayout, q.Get("to"))
		if err1 != nil || err2 != nil {
			writeError(w, http.StatusBadRequest, "from and to must both be YYYY-MM-DD")
			return
		}
	}
	rep, err := s.svc.Report(r.Context(), g.DiscordID, from, to)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := reportDTO{
		DiscordGuildID: rep.GuildDiscordID, From: rep.From.Format(dateLayout), To: rep.To.Format(dateLayout),
		Anonymous: rep.Anonymous, Responses: rep.Responses, Respondents: rep.Respondents,
		Average: rep.Average, Distribution: map[string]int{},
		Days: []dayDTO{}, Messages: []reportMsgDTO{},
		Kudos: kudosSummaryDTO{
			Count: rep.Kudos.Count, Givers: rep.Kudos.Givers, Receivers: rep.Kudos.Receivers, Highlights: []kudosDTO{},
		},
	}
	for _, k := range rep.Kudos.Highlights {
		out.Kudos.Highlights = append(out.Kudos.Highlights, toKudosDTO(k))
	}
	for m, n := range rep.Distribution {
		out.Distribution[strconv.Itoa(int(m))] = n
	}
	for _, d := range rep.Days {
		out.Days = append(out.Days, dayDTO{Day: d.Day.Format(dateLayout), Responses: d.Responses, Average: d.Average})
	}
	for _, m := range rep.Messages {
		out.Messages = append(out.Messages, reportMsgDTO{
			Day: m.Day.Format(dateLayout), Mood: int(m.Mood), Message: m.Message,
			DiscordID: m.MemberDiscordID, Username: m.Username,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// --- Kudos ---

// giveKudos answers 201 with the stored kudos, 403 when either user isn't a known member of
// the guild and 429 when the giver reached today's limit.
func (s *Server) giveKudos(w http.ResponseWriter, r *http.Request) {
	var in struct {
		From    string `json:"from_discord_user_id"`
		To      string `json:"to_discord_user_id"`
		Message string `json:"message"`
	}
	if !decode(w, r, &in) {
		return
	}
	k, err := s.svc.GiveKudos(r.Context(), usecase.GiveKudosInput{
		GuildID: r.PathValue("guildID"), FromUserID: in.From, ToUserID: in.To, Message: in.Message,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toKudosDTO(k))
}

// --- Dispatches ---

// claimDispatch answers 201 when the caller should send the survey or report for that period,
// and 409 when it was already sent.
func (s *Server) claimDispatch(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Kind   string `json:"kind"`
		Period string `json:"period"` // YYYY-MM-DD: the survey day, or the first day of the report period
	}
	if !decode(w, r, &in) {
		return
	}
	period, err := time.Parse(dateLayout, in.Period)
	if err != nil {
		writeError(w, http.StatusBadRequest, "period must be YYYY-MM-DD")
		return
	}
	claimed, err := s.svc.ClaimDispatch(r.Context(), r.PathValue("guildID"), domain.DispatchKind(in.Kind), period)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !claimed {
		writeError(w, http.StatusConflict, "already dispatched")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"kind": in.Kind, "period": in.Period})
}

// patchWorkingDays turns the ISO day numbers of a settings change into p.WorkingDays, and
// answers 400 if they are not valid.
func (s *Server) patchWorkingDays(w http.ResponseWriter, r *http.Request, iso *[]int, p *usecase.SettingsPatch) bool {
	if iso == nil {
		return true
	}
	days, err := domain.WeekdaysOf(*iso)
	if err != nil {
		s.fail(w, r, err)
		return false
	}
	p.WorkingDays = &days
	return true
}
