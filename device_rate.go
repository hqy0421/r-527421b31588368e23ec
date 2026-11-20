package agouti

import (
	"fmt"
	"sort"
	"time"
)

// ErrorKind classifies a failure of device rate analysis. Every failure of
// AnalyzeDeviceRates is reported as an *AnalysisError carrying one of these
// kinds, so callers can distinguish bad data from bad specifications and
// from models that cannot be estimated.
type ErrorKind string

const (
	// TypeError indicates malformed input data: an unparseable timestamp, a
	// non-numeric value for a numeric covariate, an interval with a
	// non-positive duration, a missing device ID, and so on.
	TypeError ErrorKind = "type error"

	// SpecError indicates an invalid analysis specification: no statistical
	// periods, overlapping periods, duplicate period names, or an unknown
	// covariate type.
	SpecError ErrorKind = "invalid specification"

	// PopulationError indicates an inconsistent device population: online
	// intervals, maintenance intervals, or events that reference devices
	// missing from the device attributes of the snapshot, or a snapshot
	// that contains no devices at all.
	PopulationError ErrorKind = "population inconsistency"

	// ConflictError indicates duplicate records that disagree: two devices
	// with the same ID but different attributes, or two events sharing a
	// stable ID but carrying different values.
	ConflictError ErrorKind = "conflicting values"

	// VersionError indicates that the read tables do not all belong to the
	// same completed snapshot version.
	VersionError ErrorKind = "snapshot version error"

	// NotEstimableError indicates that the Poisson model cannot be
	// estimated from the panel: no rows with positive exposure, no observed
	// events, a rank-deficient design matrix, or a fit that does not
	// converge.
	NotEstimableError ErrorKind = "model not estimable"
)

// An AnalysisError describes a failed device rate analysis.
type AnalysisError struct {
	Kind    ErrorKind
	Message string
}

func (e *AnalysisError) Error() string {
	return fmt.Sprintf("device rate analysis failed (%s): %s", e.Kind, e.Message)
}

func newAnalysisError(kind ErrorKind, format string, args ...interface{}) *AnalysisError {
	return &AnalysisError{Kind: kind, Message: fmt.Sprintf(format, args...)}
}

// A Device is one member of the analyzed population. Covariates holds the
// device attributes read from the snapshot; the analysis uses the subset
// declared in AnalysisSpec.
type Device struct {
	ID         string
	Covariates map[string]string
}

// An Interval is a half-open [Start, End) span of time during which a
// device was online or under maintenance. Timestamps are normalized to UTC
// by the analysis.
type Interval struct {
	DeviceID string
	Start    time.Time
	End      time.Time
}

// A DeviceEvent is a single occurrence (for example, an alarm) identified
// by a stable ID. Events sharing an ID are counted once; the same ID
// carrying different values is a ConflictError.
type DeviceEvent struct {
	ID       string
	DeviceID string
	Time     time.Time
	Kind     string
}

// A DeviceSnapshot is the complete, same-version input for a device rate
// analysis: the device population with its attributes, the online and
// maintenance intervals, and the stable-ID events. ReadDeviceSnapshot
// builds one from a logged-in page; it may also be constructed directly.
type DeviceSnapshot struct {
	Version     string
	Devices     []Device
	Online      []Interval
	Maintenance []Interval
	Events      []DeviceEvent
}

// A Period is a declared half-open [Start, End) statistical period. Periods
// must not overlap; gaps between periods are allowed and simply accumulate
// no exposure. An empty Name is replaced by a "[start, end)" label.
type Period struct {
	Name  string
	Start time.Time
	End   time.Time
}

// CovariateType declares how a device attribute enters the Poisson model.
type CovariateType string

const (
	// Categorical covariates enter the model as dummy variables; the
	// alphabetically first observed level is the reference level.
	Categorical CovariateType = "categorical"

	// Numeric covariates enter the model as a single continuous term and
	// every device value must parse as a finite float.
	Numeric CovariateType = "numeric"
)

// A Covariate declares a device attribute used by the Poisson model and by
// the grouped rate report.
type Covariate struct {
	Name string
	Type CovariateType
}

// An AnalysisSpec declares the statistical periods and the covariates used
// for a device rate analysis.
type AnalysisSpec struct {
	Periods    []Period
	Covariates []Covariate
}

// A PanelRow is one cell of the complete device x period panel. Exposure is
// the effective online time (maintenance deducted) in seconds. Rate is
// Events/Exposure when Exposure is positive, and nil otherwise: a device
// with no exposure has no rate, not a fabricated zero.
type PanelRow struct {
	DeviceID   string
	Period     string
	Exposure   float64
	Events     int
	Rate       *float64
	Covariates map[string]string
}

// ExclusionReason explains why an event was not counted.
type ExclusionReason string

const (
	// ExcludedOffline: the event fired while its device was not covered by
	// any online interval.
	ExcludedOffline ExclusionReason = "offline"

	// ExcludedMaintenance: the event fired while its device was inside a
	// maintenance interval.
	ExcludedMaintenance ExclusionReason = "maintenance"

	// ExcludedOutsidePeriods: the event fired while its device was online
	// and not under maintenance, but outside every declared period.
	ExcludedOutsidePeriods ExclusionReason = "outside_periods"
)

// An Exclusion records an event that was not counted, with the reason.
type Exclusion struct {
	EventID  string
	DeviceID string
	Time     time.Time
	Reason   ExclusionReason
}

// A ModelRow is one row actually fed into the Poisson model: a panel row
// with positive exposure. Offset is the natural logarithm of Exposure and
// Predicted is the fitted expected event count.
type ModelRow struct {
	DeviceID   string
	Period     string
	Exposure   float64
	Events     int
	Offset     float64
	Predicted  float64
	Covariates map[string]string
}

// A Coefficient is one estimated term of the Poisson model. Names are
// "(Intercept)", "name=level" for categorical terms, and "name" for
// numeric terms.
type Coefficient struct {
	Name     string
	Estimate float64
	StdErr   float64
}

// A PoissonFit is the result of fitting the Poisson regression with a log
// link and the log of effective exposure as offset.
type PoissonFit struct {
	Coefficients     []Coefficient
	Iterations       int
	Deviance         float64
	DegreesOfFreedom int
}

// A GroupRate aggregates the panel over one combination of declared
// covariate values. Rate is the pooled Events/Exposure ratio (never an
// average of per-row rates) and is nil when the group has no exposure.
type GroupRate struct {
	Covariates map[string]string
	Devices    int
	Exposure   float64
	Events     int
	Rate       *float64
}

// A RateAnalysis is the complete result of a device rate analysis.
type RateAnalysis struct {
	Version    string
	Periods    []Period
	Panel      []PanelRow
	Exclusions []Exclusion
	ModelRows  []ModelRow
	Model      *PoissonFit
	GroupRates []GroupRate
}

// AnalyzeDeviceRates turns a device snapshot into event rates per unit of
// effective exposure.
//
// Online intervals are normalized to UTC, merged where they overlap or
// touch, and reduced by the maintenance intervals. The remaining effective
// exposure is split over the declared half-open periods. Events are
// deduplicated by their stable ID (conflicting duplicates are an error)
// and counted in the device-period they fall in; events that fire while
// their device is offline, under maintenance, or outside every declared
// period are excluded with a recorded reason.
//
// The complete device x period panel is returned: rows with positive
// exposure but no events keep a real zero rate, while rows without
// exposure carry a nil rate. A Poisson regression is fit to the exposed
// rows with the declared covariates and the log of effective exposure as
// offset, and the grouped rate report pools events and exposure per
// covariate combination.
//
// Any type error, population inconsistency, conflicting duplicate, or
// non-estimable model fails the whole analysis with an *AnalysisError.
func AnalyzeDeviceRates(snapshot *DeviceSnapshot, spec AnalysisSpec) (*RateAnalysis, error) {
	if snapshot == nil {
		return nil, newAnalysisError(SpecError, "snapshot must not be nil")
	}
	periods, err := validatePeriods(spec.Periods)
	if err != nil {
		return nil, err
	}
	if err := validateCovariates(spec.Covariates); err != nil {
		return nil, err
	}
	devices, err := collectDevices(snapshot.Devices)
	if err != nil {
		return nil, err
	}
	if len(devices) == 0 {
		return nil, newAnalysisError(PopulationError, "snapshot contains no devices")
	}
	if err := validateDeviceCovariates(devices, spec.Covariates); err != nil {
		return nil, err
	}
	online, err := collectIntervals(snapshot.Online, devices, "online")
	if err != nil {
		return nil, err
	}
	maintenance, err := collectIntervals(snapshot.Maintenance, devices, "maintenance")
	if err != nil {
		return nil, err
	}
	events, err := collectEvents(snapshot.Events, devices)
	if err != nil {
		return nil, err
	}

	mergedOnline := map[string][]span{}
	mergedMaintenance := map[string][]span{}
	effective := map[string][]span{}
	for id := range devices {
		mergedOnline[id] = mergeSpans(online[id])
		mergedMaintenance[id] = mergeSpans(maintenance[id])
		effective[id] = subtractSpans(mergedOnline[id], mergedMaintenance[id])
	}

	exposure := map[string][]float64{}
	for id := range devices {
		exposure[id] = make([]float64, len(periods))
		for _, active := range effective[id] {
			for i, period := range periods {
				start := maxTime(active.start, period.Start)
				end := minTime(active.end, period.End)
				if end.After(start) {
					exposure[id][i] += end.Sub(start).Seconds()
				}
			}
		}
	}

	counts := map[string]map[int]int{}
	var exclusions []Exclusion
	for _, event := range events {
		at := event.Time.UTC()
		switch {
		case !coveredBy(mergedOnline[event.DeviceID], at):
			exclusions = append(exclusions, Exclusion{event.ID, event.DeviceID, at, ExcludedOffline})
		case coveredBy(mergedMaintenance[event.DeviceID], at):
			exclusions = append(exclusions, Exclusion{event.ID, event.DeviceID, at, ExcludedMaintenance})
		default:
			index := periodIndex(periods, at)
			if index < 0 {
				exclusions = append(exclusions, Exclusion{event.ID, event.DeviceID, at, ExcludedOutsidePeriods})
				continue
			}
			if counts[event.DeviceID] == nil {
				counts[event.DeviceID] = map[int]int{}
			}
			counts[event.DeviceID][index]++
		}
	}

	var panel []PanelRow
	for _, id := range sortedDeviceIDs(devices) {
		for i, period := range periods {
			row := PanelRow{
				DeviceID:   id,
				Period:     period.Name,
				Exposure:   exposure[id][i],
				Events:     counts[id][i],
				Covariates: declaredCovariates(devices[id], spec.Covariates),
			}
			if row.Exposure > 0 {
				rate := float64(row.Events) / row.Exposure
				row.Rate = &rate
			}
			panel = append(panel, row)
		}
	}

	var modelRows []ModelRow
	for _, row := range panel {
		if row.Exposure <= 0 {
			continue
		}
		modelRows = append(modelRows, ModelRow{
			DeviceID:   row.DeviceID,
			Period:     row.Period,
			Exposure:   row.Exposure,
			Events:     row.Events,
			Offset:     logOffset(row.Exposure),
			Covariates: row.Covariates,
		})
	}
	if len(modelRows) == 0 {
		return nil, newAnalysisError(NotEstimableError, "no device-period rows with positive exposure")
	}
	model, err := fitPoissonModel(modelRows, spec.Covariates)
	if err != nil {
		return nil, err
	}

	return &RateAnalysis{
		Version:    snapshot.Version,
		Periods:    periods,
		Panel:      panel,
		Exclusions: exclusions,
		ModelRows:  modelRows,
		Model:      model,
		GroupRates: buildGroupRates(panel, spec.Covariates),
	}, nil
}

// span is a half-open [start, end) UTC time interval.
type span struct {
	start time.Time
	end   time.Time
}

func validatePeriods(declared []Period) ([]Period, error) {
	if len(declared) == 0 {
		return nil, newAnalysisError(SpecError, "at least one statistical period must be declared")
	}
	periods := make([]Period, len(declared))
	names := map[string]bool{}
	for i, period := range declared {
		start, end := period.Start.UTC(), period.End.UTC()
		if !end.After(start) {
			return nil, newAnalysisError(SpecError, "period %d has non-positive duration [%s, %s)", i+1, start, end)
		}
		name := period.Name
		if name == "" {
			name = fmt.Sprintf("[%s, %s)", start.Format(time.RFC3339), end.Format(time.RFC3339))
		}
		if names[name] {
			return nil, newAnalysisError(SpecError, "duplicate period name %q", name)
		}
		names[name] = true
		periods[i] = Period{Name: name, Start: start, End: end}
	}
	byStart := append([]Period(nil), periods...)
	sort.Slice(byStart, func(i, j int) bool { return byStart[i].Start.Before(byStart[j].Start) })
	for i := 1; i < len(byStart); i++ {
		if byStart[i].Start.Before(byStart[i-1].End) {
			return nil, newAnalysisError(SpecError, "declared periods %q and %q overlap", byStart[i-1].Name, byStart[i].Name)
		}
	}
	return periods, nil
}

func validateCovariates(covariates []Covariate) error {
	names := map[string]bool{}
	for _, covariate := range covariates {
		if covariate.Name == "" {
			return newAnalysisError(SpecError, "covariate with empty name")
		}
		if names[covariate.Name] {
			return newAnalysisError(SpecError, "duplicate covariate %q", covariate.Name)
		}
		names[covariate.Name] = true
		switch covariate.Type {
		case Categorical, Numeric:
		default:
			return newAnalysisError(SpecError, "covariate %q has unknown type %q", covariate.Name, covariate.Type)
		}
	}
	return nil
}

func collectDevices(declared []Device) (map[string]Device, error) {
	devices := map[string]Device{}
	for _, device := range declared {
		if device.ID == "" {
			return nil, newAnalysisError(TypeError, "device with empty ID")
		}
		existing, seen := devices[device.ID]
		if seen {
			if !sameCovariates(existing.Covariates, device.Covariates) {
				return nil, newAnalysisError(ConflictError, "device %q is declared twice with conflicting attributes", device.ID)
			}
			continue
		}
		covariates := map[string]string{}
		for name, value := range device.Covariates {
			covariates[name] = value
		}
		devices[device.ID] = Device{ID: device.ID, Covariates: covariates}
	}
	return devices, nil
}

func validateDeviceCovariates(devices map[string]Device, covariates []Covariate) error {
	for _, id := range sortedDeviceIDs(devices) {
		for _, covariate := range covariates {
			value, ok := devices[id].Covariates[covariate.Name]
			if !ok {
				return newAnalysisError(TypeError, "device %q is missing declared covariate %q", id, covariate.Name)
			}
			if covariate.Type == Numeric && !isFiniteFloat(value) {
				return newAnalysisError(TypeError, "device %q has non-numeric value %q for numeric covariate %q", id, value, covariate.Name)
			}
		}
	}
	return nil
}

func collectIntervals(declared []Interval, devices map[string]Device, kind string) (map[string][]span, error) {
	intervals := map[string][]span{}
	for i, interval := range declared {
		if _, ok := devices[interval.DeviceID]; !ok {
			return nil, newAnalysisError(PopulationError, "%s interval %d references unknown device %q", kind, i+1, interval.DeviceID)
		}
		start, end := interval.Start.UTC(), interval.End.UTC()
		if !end.After(start) {
			return nil, newAnalysisError(TypeError, "%s interval for device %q has non-positive duration [%s, %s)", kind, interval.DeviceID, start, end)
		}
		intervals[interval.DeviceID] = append(intervals[interval.DeviceID], span{start, end})
	}
	return intervals, nil
}

func collectEvents(declared []DeviceEvent, devices map[string]Device) ([]DeviceEvent, error) {
	seen := map[string]DeviceEvent{}
	var events []DeviceEvent
	for _, event := range declared {
		if event.ID == "" {
			return nil, newAnalysisError(TypeError, "event with empty stable ID")
		}
		if _, ok := devices[event.DeviceID]; !ok {
			return nil, newAnalysisError(PopulationError, "event %q references unknown device %q", event.ID, event.DeviceID)
		}
		event.Time = event.Time.UTC()
		if existing, ok := seen[event.ID]; ok {
			if existing.DeviceID != event.DeviceID || !existing.Time.Equal(event.Time) || existing.Kind != event.Kind {
				return nil, newAnalysisError(ConflictError, "event %q is reported with conflicting values", event.ID)
			}
			continue
		}
		seen[event.ID] = event
		events = append(events, event)
	}
	sort.Slice(events, func(i, j int) bool {
		if !events[i].Time.Equal(events[j].Time) {
			return events[i].Time.Before(events[j].Time)
		}
		return events[i].ID < events[j].ID
	})
	return events, nil
}

// mergeSpans merges overlapping or touching spans. The input is sorted in
// place; the result is a sorted, disjoint slice.
func mergeSpans(spans []span) []span {
	if len(spans) == 0 {
		return nil
	}
	sort.Slice(spans, func(i, j int) bool {
		if !spans[i].start.Equal(spans[j].start) {
			return spans[i].start.Before(spans[j].start)
		}
		return spans[i].end.Before(spans[j].end)
	})
	merged := []span{spans[0]}
	for _, next := range spans[1:] {
		last := &merged[len(merged)-1]
		if next.start.After(last.end) {
			merged = append(merged, next)
			continue
		}
		if next.end.After(last.end) {
			last.end = next.end
		}
	}
	return merged
}

// subtractSpans removes the cuts from the base spans. Both inputs must be
// merged and sorted; the result is sorted and disjoint.
func subtractSpans(base, cuts []span) []span {
	var result []span
	for _, b := range base {
		segments := []span{b}
		for _, c := range cuts {
			var remaining []span
			for _, s := range segments {
				if !c.end.After(s.start) || !c.start.Before(s.end) {
					remaining = append(remaining, s)
					continue
				}
				if c.start.After(s.start) {
					remaining = append(remaining, span{s.start, minTime(c.start, s.end)})
				}
				if c.end.Before(s.end) {
					remaining = append(remaining, span{maxTime(c.end, s.start), s.end})
				}
			}
			segments = remaining
		}
		result = append(result, segments...)
	}
	return result
}

func coveredBy(spans []span, at time.Time) bool {
	for _, s := range spans {
		if !at.Before(s.start) && at.Before(s.end) {
			return true
		}
	}
	return false
}

func periodIndex(periods []Period, at time.Time) int {
	for i, period := range periods {
		if !at.Before(period.Start) && at.Before(period.End) {
			return i
		}
	}
	return -1
}

func buildGroupRates(panel []PanelRow, covariates []Covariate) []GroupRate {
	type accumulator struct {
		covariates map[string]string
		devices    map[string]bool
		exposure   float64
		events     int
	}
	groups := map[string]*accumulator{}
	var keys []string
	for _, row := range panel {
		key := groupKey(row.Covariates, covariates)
		acc, ok := groups[key]
		if !ok {
			acc = &accumulator{covariates: row.Covariates, devices: map[string]bool{}}
			groups[key] = acc
			keys = append(keys, key)
		}
		acc.devices[row.DeviceID] = true
		acc.exposure += row.Exposure
		acc.events += row.Events
	}
	sort.Strings(keys)
	var rates []GroupRate
	for _, key := range keys {
		acc := groups[key]
		rate := GroupRate{
			Covariates: acc.covariates,
			Devices:    len(acc.devices),
			Exposure:   acc.exposure,
			Events:     acc.events,
		}
		if acc.exposure > 0 {
			pooled := float64(acc.events) / acc.exposure
			rate.Rate = &pooled
		}
		rates = append(rates, rate)
	}
	return rates
}

func groupKey(values map[string]string, covariates []Covariate) string {
	key := ""
	for i, covariate := range covariates {
		if i > 0 {
			key += "\x00"
		}
		key += values[covariate.Name]
	}
	return key
}

func declaredCovariates(device Device, covariates []Covariate) map[string]string {
	values := map[string]string{}
	for _, covariate := range covariates {
		values[covariate.Name] = device.Covariates[covariate.Name]
	}
	return values
}

func sortedDeviceIDs(devices map[string]Device) []string {
	var ids []string
	for id := range devices {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func sameCovariates(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for name, value := range a {
		if other, ok := b[name]; !ok || other != value {
			return false
		}
	}
	return true
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
