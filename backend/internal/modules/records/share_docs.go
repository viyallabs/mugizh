package records

import (
	"context"
	"time"

	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/records/public"
)

// SharedDocuments is a patient's upload list as a doctor with a history share sees it.
type SharedDocuments struct {
	Items []rp.DocumentInfo
	// AccessUntil is when the doctor's access ends (nil if the access has no end, which no
	// patient share has); the app warns the doctor before it.
	AccessUntil *time.Time
	GrantID     string
}

// ListPatientDocuments lists a patient's uploads (metadata only) for a doctor. It goes through
// Authorize with KindDocuments, so the read is audited, a doctor with no relationship gets
// ErrNotFound and an ended or missing share gets ErrDenied (consent.required). A patient reading
// their own list uses ListDocuments.
func (r *Rights) ListPatientDocuments(ctx context.Context, c rp.Caller, patientRef string) (SharedDocuments, error) {
	if !allUUID(c.UserRef, patientRef) || c.Role == "patient" {
		return SharedDocuments{}, rp.ErrNotFound
	}
	d, err := r.m.Authorize(ctx, rp.Request{Actor: rp.Actor{Ref: c.UserRef, Role: c.Role, ClinicRef: c.ClinicRef, Restricted: c.Restricted},
		PatientRef: patientRef, Kind: rp.KindDocuments, ResourceType: "document", RequestID: c.RequestID, Client: c.Client, IPHash: c.IPHash})
	if err != nil {
		return SharedDocuments{}, err
	}
	if !d.Allowed {
		if d.Hide {
			return SharedDocuments{}, rp.ErrNotFound
		}
		return SharedDocuments{}, rp.ErrDenied
	}
	out := SharedDocuments{GrantID: d.GrantID}
	if until, ok, err := r.m.HistoryAccessUntil(ctx, patientRef, c.UserRef); err != nil {
		return SharedDocuments{}, err
	} else if ok {
		out.AccessUntil = &until
	}
	if out.Items, err = r.documentsOf(ctx, r.m.pool, patientRef); err != nil {
		return SharedDocuments{}, err
	}
	return out, nil
}

// Page returns at most limit documents after the document with ID after (empty for the first
// page) and the cursor for the next page ("" on the last). Documents are ordered by upload time.
func (d SharedDocuments) Page(limit int, after string) (items []rp.DocumentInfo, next string) {
	limit = min(max(limit, 1), 100)
	start := 0
	if after != "" {
		start = len(d.Items)
		for i, x := range d.Items {
			if x.ID == after {
				start = i + 1
				break
			}
		}
	}
	rest := d.Items[start:]
	if len(rest) > limit {
		return rest[:limit], rest[limit-1].ID
	}
	return rest, ""
}
