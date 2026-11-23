package rules

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"time"
	_ "time/tzdata" // zone data travels with the binary: the follow-up window uses the patient's zone
)

const maxItems = 100

var (
	reItemRef = regexp.MustCompile(`^[A-Za-z0-9_\-]{1,80}$`)
	reDrugRef = regexp.MustCompile(`^[a-z0-9][a-z0-9_\-]{0,79}$`)
	reToken   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_/%.\-]{0,19}$`)
)

// Check decides whether the prescription may be issued. It never panics and never returns
// a suggestion: on any doubt it blocks (K-CS-02, fail closed).
func (e *Engine) Check(in Input) (res Result) {
	defer func() {
		if r := recover(); r != nil {
			res = e.failed(in, CodeInternalError)
		}
	}()

	stage := in.Stage
	if stage == "" {
		stage = StageSign
	}
	base := Result{RuleSetVersion: e.f.Version, RuleSetChecksum: e.checksum, Mode: in.Mode, Stage: stage, ConsultType: ConsultFirst}
	if in.Classification != nil {
		base.DrugClassificationVersion = in.Classification.Version()
	}
	bad := func(code string) Result {
		r := e.failed(in, code)
		r.Stage = stage
		r.DrugClassificationVersion = base.DrugClassificationVersion
		return r
	}

	switch {
	case stage != StageDraft && stage != StageSign:
		return bad(CodeInputInvalid)
	case in.RuleSetVersion != e.f.Version:
		return bad(CodeRuleSetMismatch)
	case in.Classification == nil || in.Classification.Version() == "":
		return bad(CodeInputInvalid)
	case !e.modes[string(in.Mode)]:
		return bad(CodeInputInvalid)
	case len(in.Items) == 0 || len(in.Items) > maxItems:
		return bad(CodeInputInvalid)
	}
	seen := map[string]bool{}
	for _, it := range in.Items {
		if !reItemRef.MatchString(it.Ref) || seen[it.Ref] {
			return bad(CodeInputInvalid)
		}
		seen[it.Ref] = true
	}
	remote := e.remote[string(in.Mode)]

	rxType := ConsultFirst
	attestMissing := false
	if remote {
		var err error
		rxType, attestMissing, err = e.followUp(in)
		if err != nil {
			return bad(CodeInputInvalid)
		}
	}
	base.ConsultType = rxType

	// Prescription-level checks, in the rule file's order.
	var structureReason string
	for _, ck := range e.checks {
		pass, applies := true, true
		switch ck.check {
		case chkDoctorVerified:
			pass = in.PrescriberVerified
		case chkIdentity:
			pass = in.IdentityConfirmed
		case chkAge:
			pass = in.PatientAgeYears > 0 && in.PatientAgeYears <= 150
		case chkConsent:
			applies = remote
			pass = e.consentOK(in)
		case chkDiagnosis:
			pass = in.DiagnosisPresent
		case chkFollowUp:
			applies = remote && in.FollowUp.Claimed
			pass = !attestMissing
		case chkStructure:
			structureReason = ck.reason // checked per item
			continue
		case chkStepUp:
			applies = stage == StageSign
			pass = in.StepUpDone
		case chkESign:
			applies = stage == StageSign
			pass = in.ESignatureApplied
		case chkPatientLocation:
			applies = remote
			pass = e.allowedCountries[in.PatientCountry]
		default:
			applies, pass = true, false // cannot happen after Load; fail closed
		}
		if !applies {
			continue
		}
		cr := CheckResult{ID: ck.id, Passed: pass}
		if !pass {
			cr.Reason = e.reason(ck.reason)
			base.Reasons = append(base.Reasons, cr.Reason)
		}
		base.Checks = append(base.Checks, cr)
	}

	allowed := len(base.Reasons) == 0
	for _, it := range in.Items {
		ir := e.checkItem(in, it, remote, rxType, structureReason)
		if !ir.Allowed {
			allowed = false
		}
		base.Items = append(base.Items, ir)
	}
	base.Allowed = allowed
	return base
}

// failed builds a blocked result for input-level problems. Every item is blocked too.
func (e *Engine) failed(in Input, code string) Result {
	r := Result{RuleSetVersion: e.f.Version, RuleSetChecksum: e.checksum, Mode: in.Mode, Stage: in.Stage, ConsultType: ConsultFirst,
		Reasons: []Reason{e.reason(code)}}
	for _, it := range in.Items {
		ir := ItemResult{Allowed: false, EffectiveConsultType: ConsultFirst, Reasons: []Reason{e.reason(code)}}
		if reItemRef.MatchString(it.Ref) {
			ir.Ref = it.Ref
		}
		r.Items = append(r.Items, ir)
	}
	return r
}

func (e *Engine) consentOK(in Input) bool {
	switch {
	case e.initImplied[in.Initiator]:
		return true
	case e.initExplicit[in.Initiator]:
		return in.ExplicitConsent
	}
	return false
}

// followUp decides whether the consult is a follow-up (spec section 3). It returns the
// consult type and whether a claimed follow-up lacks its attestations (P06). A claim that
// fails the same-doctor, prior in-person visit or window test is simply a first consult
// (T08). An error means the input cannot be evaluated.
func (e *Engine) followUp(in Input) (ConsultType, bool, error) {
	f := in.FollowUp
	if !f.Claimed {
		return ConsultFirst, false, nil
	}
	loc, err := time.LoadLocation(in.PatientTimeZone)
	if err != nil || in.PatientTimeZone == "" || in.PatientTimeZone == "Local" || in.ConsultAt.IsZero() {
		return ConsultFirst, false, fmt.Errorf("rules: follow-up needs the patient's time zone and the consult time")
	}
	if !f.SameDoctor || f.LastInPersonAt.IsZero() || !e.evidence[f.Evidence] {
		return ConsultFirst, false, nil
	}
	last := civilOf(f.LastInPersonAt.In(loc))
	today := civilOf(in.ConsultAt.In(loc))
	end := last.addMonthsClamped(e.months)
	if today.before(last) || end.before(today) { // the end day is included
		return ConsultFirst, false, nil
	}
	if !(f.SameConditionAttested && f.NoNewSymptomsAttested && f.RecallsContextAttested) {
		return ConsultFirst, true, nil
	}
	return ConsultFollowUp, false, nil
}

// civil is a calendar date.
type civil struct{ y, m, d int }

func civilOf(t time.Time) civil { return civil{t.Year(), int(t.Month()), t.Day()} }

func (c civil) key() int { return c.y*10000 + c.m*100 + c.d }

func (c civil) before(o civil) bool { return c.key() < o.key() }

func daysIn(y, m int) int { return time.Date(y, time.Month(m)+1, 0, 0, 0, 0, 0, time.UTC).Day() }

// addMonthsClamped adds calendar months; a day past the end of the target month moves to
// that month's last day (31 Aug + 6 months = the last day of February).
func (c civil) addMonthsClamped(months int) civil {
	total := c.y*12 + (c.m - 1) + months
	y, m := total/12, total%12+1
	d := c.d
	if dim := daysIn(y, m); d > dim {
		d = dim
	}
	return civil{y, m, d}
}

// effective works out the categories that count for a drug, applying the precedence
// rule: "prohibited" wins over every list unless the amendment exception names the drug.
func (e *Engine) effective(d Drug) (cats map[string]bool, prohibited, unclassified bool) {
	prohibited = d.Prohibited
	if prohibited && d.AmendmentException && e.except[NormalizeName(d.GenericName)] {
		prohibited = false
	}
	cats = map[string]bool{}
	if prohibited {
		cats[catProhibited] = true
		return cats, true, false
	}
	cats[catListO], cats[catListA], cats[catListB] = d.ListO, d.ListA, d.ListB
	unclassified = !d.ListO && !d.ListA && !d.ListB
	return cats, false, unclassified
}

func (e *Engine) modeMatch(vals []string, m Mode) bool {
	for _, v := range vals {
		if v == "any" || v == string(m) || (v == "remote" && e.remote[string(m)]) {
			return true
		}
	}
	return false
}

func anyOr(vals []string, v string) bool {
	return slices.Contains(vals, "any") || slices.Contains(vals, v)
}

func (e *Engine) checkItem(in Input, it Item, remote bool, rxType ConsultType, structureReason string) ItemResult {
	ir := ItemResult{Ref: it.Ref, DrugRef: it.DrugRef, EffectiveConsultType: ConsultFirst}
	block := func(code string) ItemResult {
		ir.Allowed = false
		ir.Reasons = append(ir.Reasons, e.reason(code))
		ir.Hits = append(ir.Hits, Hit{Layer: LayerEngine, Reason: code})
		return ir
	}
	if !reDrugRef.MatchString(it.DrugRef) {
		return block(CodeInputInvalid)
	}
	ctx := it.Context
	if remote {
		switch ctx {
		case ContextNewDrug, ContextAddOn:
		case ContextRefill:
			if !it.PriorPrescribed {
				ctx = ContextNewDrug // history does not support a refill: the stricter reading
			}
		default:
			return block(CodeInputInvalid)
		}
		ir.ContextUsed = ctx
	} else {
		ctx = ""
	}
	if rxType == ConsultFollowUp && !it.ForNewCondition {
		ir.EffectiveConsultType = ConsultFollowUp
	}

	drug, ok := in.Classification.Lookup(it.DrugRef)
	if !ok {
		return block(CodeUnknownDrug)
	}
	cats, prohibited, unclassified := e.effective(drug)

	// Layer 1: regulatory table, first matching row wins, no match blocks.
	matched, regAllow, regReason := false, false, ""
	for _, r := range e.reg {
		if !e.regMatches(r, cats, ir.EffectiveConsultType, in.Mode, ctx) {
			continue
		}
		matched, regAllow, regReason = true, r.allow, r.reason
		ir.Hits = append(ir.Hits, Hit{Layer: LayerRegulatory, RuleID: r.id, Allowed: r.allow, Reason: r.reason})
		break
	}
	if !matched {
		ir.Hits = append(ir.Hits, Hit{Layer: LayerRegulatory, Reason: CodeNoRuleMatched})
	}

	// Layer 2: platform rules, all that apply.
	var platReasons []string
	for _, p := range e.plat {
		if !e.modeMatch(p.modes, in.Mode) {
			continue
		}
		hit := (p.category == catProhibited && prohibited) || (p.category == catUnclassified && unclassified) ||
			(len(p.names) > 0 && slices.Contains(p.names, NormalizeName(drug.GenericName)))
		if hit {
			platReasons = append(platReasons, p.reason)
			ir.Hits = append(ir.Hits, Hit{Layer: LayerPlatform, RuleID: p.id, Reason: p.reason})
		}
	}

	var reasons []string
	switch {
	case matched && !regAllow:
		reasons = append(reasons, regReason)
	case !matched && len(platReasons) == 0:
		reasons = append(reasons, CodeNoRuleMatched)
	}
	reasons = append(reasons, platReasons...)

	// P07: structured dose fields, never inferred.
	if !structureComplete(it) {
		reasons = append(reasons, structureReason)
		ir.Hits = append(ir.Hits, Hit{Layer: LayerCheck, RuleID: "P07", Reason: structureReason})
	}

	seen := map[string]bool{}
	for _, c := range reasons {
		if c != "" && !seen[c] {
			seen[c] = true
			ir.Reasons = append(ir.Reasons, e.reason(c))
		}
	}
	ir.Allowed = len(ir.Reasons) == 0 && matched && regAllow
	return ir
}

func (e *Engine) regMatches(r regRule, cats map[string]bool, ct ConsultType, m Mode, ctx ItemContext) bool {
	catOK := slices.Contains(r.cats, catAny)
	for _, c := range r.cats {
		if cats[c] {
			catOK = true
		}
	}
	return catOK && anyOr(r.types, string(ct)) && e.modeMatch(r.modes, m) && anyOr(r.contexts, string(ctx))
}

func doseOK(d Dose) bool {
	return d.Amount > 0 && !math.IsNaN(d.Amount) && !math.IsInf(d.Amount, 0) && reToken.MatchString(d.Unit)
}
func freqOK(f Frequency) bool { return f.Times > 0 && f.Times <= 1000 && reToken.MatchString(f.Period) }
func durOK(d Duration) bool   { return d.Value > 0 && d.Value <= 100000 && reToken.MatchString(d.Unit) }

// structureComplete is check P07: dose, frequency, duration and route are all present as
// structured values; tapering steps are as complete as the main dose.
func structureComplete(it Item) bool {
	if !doseOK(it.Dose) || !freqOK(it.Frequency) || !durOK(it.Duration) || !reToken.MatchString(it.Route) {
		return false
	}
	for _, s := range it.Taper {
		if !doseOK(s.Dose) || !freqOK(s.Frequency) || !durOK(s.Duration) {
			return false
		}
	}
	return true
}
