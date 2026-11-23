package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/notifications"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

// Handlers implements StrictServerInterface for the patient's notification preferences.
type Handlers struct{ M *notifications.Module }

var _ StrictServerInterface = Handlers{}

var errInvalid = problem.New(http.StatusBadRequest, "notifications.invalid_preferences", "The preferences are not valid")

func patient(ctx context.Context) (*ip.Principal, error) {
	p, ok := ip.FromContext(ctx)
	if !ok || p.ActingRole != ip.RolePatient {
		return nil, problem.Forbidden
	}
	return p, nil
}

func toAPI(p notifications.Preferences) NotificationPreferences {
	out := NotificationPreferences{Channels: []ChannelChoice{}, Discreet: p.Discreet, QuietHoursEnabled: p.QuietHoursEnabled, TimeZone: p.TimeZone, Locale: p.Locale}
	for typ, chs := range p.Channels {
		c := ChannelChoice{MessageType: typ, Channels: []ChannelChoiceChannels{}}
		for _, ch := range chs {
			c.Channels = append(c.Channels, ChannelChoiceChannels(ch))
		}
		out.Channels = append(out.Channels, c)
	}
	return out
}

// GetNotificationPreferences returns the caller's preferences.
func (h Handlers) GetNotificationPreferences(ctx context.Context, _ GetNotificationPreferencesRequestObject) (GetNotificationPreferencesResponseObject, error) {
	p, err := patient(ctx)
	if err != nil {
		return nil, err
	}
	prefs, err := h.M.Preferences(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	return GetNotificationPreferences200JSONResponse(toAPI(prefs)), nil
}

// UpdateNotificationPreferences replaces the caller's preferences.
func (h Handlers) UpdateNotificationPreferences(ctx context.Context, req UpdateNotificationPreferencesRequestObject) (UpdateNotificationPreferencesResponseObject, error) {
	p, err := patient(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errInvalid
	}
	prefs := notifications.Preferences{Channels: map[string][]notifications.Channel{}, Discreet: req.Body.Discreet,
		QuietHoursEnabled: req.Body.QuietHoursEnabled, TimeZone: req.Body.TimeZone, Locale: req.Body.Locale}
	for _, c := range req.Body.Channels {
		for _, ch := range c.Channels {
			prefs.Channels[c.MessageType] = append(prefs.Channels[c.MessageType], notifications.Channel(ch))
		}
		if _, ok := prefs.Channels[c.MessageType]; !ok {
			prefs.Channels[c.MessageType] = nil
		}
	}
	if err := h.M.SetPreferences(ctx, p.UserID, prefs); err != nil {
		if errors.Is(err, notifications.ErrInvalidPreferences) {
			return nil, errInvalid
		}
		return nil, err
	}
	saved, err := h.M.Preferences(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	return UpdateNotificationPreferences200JSONResponse(toAPI(saved)), nil
}

func toAPIReminder(r notifications.ReminderOccurrence) (ReminderOccurrence, error) {
	id, err := uuid.Parse(r.ID)
	if err != nil {
		return ReminderOccurrence{}, err
	}
	return ReminderOccurrence{Id: id, Kind: ReminderOccurrenceKind(r.Kind), DueAt: r.DueAt, TimeZone: r.TimeZone, State: ReminderOccurrenceState(r.State),
		ActedAt: r.ActedAt, SnoozedUntil: r.SnoozedUntil, SnoozeCount: r.SnoozeCount, Label: r.Label, NotificationText: r.NotificationText}, nil
}

// ListMyReminders returns the caller's reminder calendar for a range (S-153).
func (h Handlers) ListMyReminders(ctx context.Context, req ListMyRemindersRequestObject) (ListMyRemindersResponseObject, error) {
	p, err := patient(ctx)
	if err != nil {
		return nil, err
	}
	rem, err := h.M.Reminders(ctx, p.UserID, req.Params.From, req.Params.To)
	if err != nil {
		return nil, err
	}
	if req.Params.IfNoneMatch != nil && strings.TrimPrefix(strings.TrimSpace(*req.Params.IfNoneMatch), "W/") == rem.ETag {
		return ListMyReminders304Response{}, nil
	}
	body := ReminderList{From: rem.From, To: rem.To, Reminders: make([]ReminderOccurrence, 0, len(rem.Items))}
	for _, it := range rem.Items {
		a, err := toAPIReminder(it)
		if err != nil {
			return nil, err
		}
		body.Reminders = append(body.Reminders, a)
	}
	etag := rem.ETag
	return ListMyReminders200JSONResponse{Body: body, Headers: ListMyReminders200ResponseHeaders{ETag: &etag}}, nil
}

// RecordReminderAction records taken, skipped or snoozed (S-154). The Idempotency-Key is the client action ID.
func (h Handlers) RecordReminderAction(ctx context.Context, req RecordReminderActionRequestObject) (RecordReminderActionResponseObject, error) {
	p, err := patient(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, problem.BadRequest
	}
	a := notifications.ReminderAction{ClientActionID: req.Params.IdempotencyKey.String(), Action: string(req.Body.Action), ActedAt: req.Body.ActedAt}
	if req.Body.SnoozeMinutes != nil {
		a.SnoozeMinutes = *req.Body.SnoozeMinutes
	}
	res, err := h.M.RecordReminderAction(ctx, p.UserID, req.OccurrenceId.String(), a)
	if err != nil {
		return nil, err
	}
	id, err := uuid.Parse(res.ID)
	if err != nil {
		return nil, err
	}
	return RecordReminderAction200JSONResponse(ReminderActionResult{Id: id, State: ReminderActionResultState(res.State), ActedAt: res.ActedAt,
		SnoozedUntil: res.SnoozedUntil, SnoozeCount: res.SnoozeCount}), nil
}

// GetMyReminderDeviceSchedule returns the caller's on-device backup schedule for the next 72 hours (S-165).
func (h Handlers) GetMyReminderDeviceSchedule(ctx context.Context, req GetMyReminderDeviceScheduleRequestObject) (GetMyReminderDeviceScheduleResponseObject, error) {
	p, err := patient(ctx)
	if err != nil {
		return nil, err
	}
	s, err := h.M.DeviceSchedule(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	if req.Params.IfNoneMatch != nil && strings.TrimPrefix(strings.TrimSpace(*req.Params.IfNoneMatch), "W/") == s.ETag {
		return GetMyReminderDeviceSchedule304Response{}, nil
	}
	body := DeviceSchedule{GeneratedAt: s.GeneratedAt, ValidUntil: s.ValidUntil, RefreshAfter: s.RefreshAfter, RemindersOn: s.RemindersOn,
		Truncated: s.Truncated, Entries: make([]DeviceScheduleEntry, 0, len(s.Entries))}
	for _, e := range s.Entries {
		out := DeviceScheduleEntry{ReplaceKey: e.ReplaceKey, FireAt: e.FireAt, TitleKey: e.TitleKey, Body: e.Body, Screen: e.Screen,
			OccurrenceIds: make([]uuid.UUID, 0, len(e.OccurrenceIDs))}
		for _, id := range e.OccurrenceIDs {
			u, err := uuid.Parse(id)
			if err != nil {
				return nil, err
			}
			out.OccurrenceIds = append(out.OccurrenceIds, u)
		}
		body.Entries = append(body.Entries, out)
	}
	etag := s.ETag
	return GetMyReminderDeviceSchedule200JSONResponse{Body: body, Headers: GetMyReminderDeviceSchedule200ResponseHeaders{ETag: &etag}}, nil
}
