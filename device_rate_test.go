package agouti_test

import (
	"math"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	. "github.com/sclevine/agouti"
)

var day0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func hoursAfter(hours float64) time.Time {
	return day0.Add(time.Duration(hours * float64(time.Hour)))
}

func dayPeriods() []Period {
	return []Period{
		{Name: "day1", Start: day0, End: hoursAfter(24)},
		{Name: "day2", Start: hoursAfter(24), End: hoursAfter(48)},
	}
}

// canonicalSnapshot builds a population of four devices with overlapping
// online intervals (one of them in a non-UTC zone), a maintenance window,
// and events covering every assignment and exclusion path.
func canonicalSnapshot() *DeviceSnapshot {
	cet := time.FixedZone("CET", 3600)
	return &DeviceSnapshot{
		Version: "v1",
		Devices: []Device{
			{ID: "d1", Covariates: map[string]string{"model": "A", "region": "EU"}},
			{ID: "d2", Covariates: map[string]string{"model": "A", "region": "US"}},
			{ID: "d3", Covariates: map[string]string{"model": "B", "region": "EU"}},
			{ID: "d4", Covariates: map[string]string{"model": "B", "region": "US"}},
		},
		Online: []Interval{
			{DeviceID: "d1", Start: day0, End: hoursAfter(24)},
			// same instants as [t0+12h, t0+36h) UTC, expressed at UTC+1
			{DeviceID: "d1", Start: time.Date(2026, 1, 1, 13, 0, 0, 0, cet), End: time.Date(2026, 1, 2, 13, 0, 0, 0, cet)},
			{DeviceID: "d2", Start: hoursAfter(24), End: hoursAfter(48)},
			{DeviceID: "d3", Start: day0, End: hoursAfter(60)},
		},
		Maintenance: []Interval{
			{DeviceID: "d3", Start: hoursAfter(12), End: hoursAfter(24)},
		},
		Events: []DeviceEvent{
			{ID: "e1", DeviceID: "d1", Time: hoursAfter(1)},
			{ID: "e9", DeviceID: "d1", Time: hoursAfter(2)},
			{ID: "e2", DeviceID: "d1", Time: hoursAfter(24)}, // exactly on the day1/day2 boundary
			{ID: "e3", DeviceID: "d1", Time: hoursAfter(25)},
			{ID: "e4", DeviceID: "d1", Time: hoursAfter(40)}, // d1 offline
			{ID: "e5", DeviceID: "d3", Time: hoursAfter(18)}, // d3 under maintenance
			{ID: "e6", DeviceID: "d2", Time: hoursAfter(30)},
			{ID: "e7", DeviceID: "d3", Time: hoursAfter(30)},
			{ID: "e8", DeviceID: "d3", Time: hoursAfter(50)}, // online but outside every period
		},
	}
}

func findPanelRow(panel []PanelRow, deviceID, period string) PanelRow {
	for _, row := range panel {
		if row.DeviceID == deviceID && row.Period == period {
			return row
		}
	}
	Fail("panel row not found: " + deviceID + " / " + period)
	return PanelRow{}
}

func expectAnalysisFailure(err error, kind ErrorKind, messagePart string) {
	Expect(err).To(HaveOccurred())
	analysisErr, ok := err.(*AnalysisError)
	Expect(ok).To(BeTrue())
	Expect(analysisErr.Kind).To(Equal(kind))
	Expect(analysisErr.Message).To(ContainSubstring(messagePart))
}

var _ = Describe("AnalyzeDeviceRates", func() {
	var spec AnalysisSpec

	BeforeEach(func() {
		spec = AnalysisSpec{Periods: dayPeriods()}
	})

	Describe("panel construction", func() {
		It("merges overlapping online intervals at unified UTC instants and deducts maintenance", func() {
			analysis, err := AnalyzeDeviceRates(canonicalSnapshot(), spec)
			Expect(err).NotTo(HaveOccurred())

			// d1: [0,24h) and [12h,36h) merge to 36h: 24h in day1, 12h in day2
			Expect(findPanelRow(analysis.Panel, "d1", "day1").Exposure).To(BeNumerically("==", 86400))
			Expect(findPanelRow(analysis.Panel, "d1", "day2").Exposure).To(BeNumerically("==", 43200))

			// d3: [0,60h) minus maintenance [12h,24h): 12h in day1, 24h in day2
			Expect(findPanelRow(analysis.Panel, "d3", "day1").Exposure).To(BeNumerically("==", 43200))
			Expect(findPanelRow(analysis.Panel, "d3", "day2").Exposure).To(BeNumerically("==", 86400))
		})

		It("assigns events to half-open periods, with boundary instants in the later period", func() {
			analysis, err := AnalyzeDeviceRates(canonicalSnapshot(), spec)
			Expect(err).NotTo(HaveOccurred())

			Expect(findPanelRow(analysis.Panel, "d1", "day1").Events).To(Equal(2))
			Expect(findPanelRow(analysis.Panel, "d1", "day2").Events).To(Equal(2)) // includes the t=24h event
			Expect(findPanelRow(analysis.Panel, "d2", "day2").Events).To(Equal(1))
			Expect(findPanelRow(analysis.Panel, "d3", "day2").Events).To(Equal(1))
		})

		It("keeps the complete device x period panel, with nil rates only where exposure is zero", func() {
			analysis, err := AnalyzeDeviceRates(canonicalSnapshot(), spec)
			Expect(err).NotTo(HaveOccurred())
			Expect(analysis.Panel).To(HaveLen(8)) // 4 devices x 2 periods

			// positive exposure and zero events: a real zero rate
			quiet := findPanelRow(analysis.Panel, "d3", "day1")
			Expect(quiet.Rate).NotTo(BeNil())
			Expect(*quiet.Rate).To(BeNumerically("==", 0))

			// no exposure: no rate, not a fabricated zero
			Expect(findPanelRow(analysis.Panel, "d2", "day1").Rate).To(BeNil())
			Expect(findPanelRow(analysis.Panel, "d4", "day1").Rate).To(BeNil())
			Expect(findPanelRow(analysis.Panel, "d4", "day2").Rate).To(BeNil())

			Expect(*findPanelRow(analysis.Panel, "d1", "day1").Rate).To(BeNumerically("~", 2.0/86400, 1e-12))
		})

		It("excludes offline, maintenance, and out-of-period events with reasons", func() {
			analysis, err := AnalyzeDeviceRates(canonicalSnapshot(), spec)
			Expect(err).NotTo(HaveOccurred())
			Expect(analysis.Exclusions).To(Equal([]Exclusion{
				{EventID: "e5", DeviceID: "d3", Time: hoursAfter(18), Reason: ExcludedMaintenance},
				{EventID: "e4", DeviceID: "d1", Time: hoursAfter(40), Reason: ExcludedOffline},
				{EventID: "e8", DeviceID: "d3", Time: hoursAfter(50), Reason: ExcludedOutsidePeriods},
			}))
		})

		It("counts duplicate events with identical values only once", func() {
			snapshot := canonicalSnapshot()
			snapshot.Events = append(snapshot.Events, DeviceEvent{ID: "e3", DeviceID: "d1", Time: hoursAfter(25)})
			analysis, err := AnalyzeDeviceRates(snapshot, spec)
			Expect(err).NotTo(HaveOccurred())
			Expect(findPanelRow(analysis.Panel, "d1", "day2").Events).To(Equal(2))
		})

		It("leaves gaps between declared periods out of exposure and events", func() {
			spec.Periods = []Period{
				{Name: "morning", Start: day0, End: hoursAfter(12)},
				{Name: "evening", Start: hoursAfter(24), End: hoursAfter(36)},
			}
			snapshot := canonicalSnapshot()
			snapshot.Events = []DeviceEvent{
				{ID: "in-gap", DeviceID: "d1", Time: hoursAfter(18)},
				{ID: "in-period", DeviceID: "d1", Time: hoursAfter(30)},
			}
			analysis, err := AnalyzeDeviceRates(snapshot, spec)
			Expect(err).NotTo(HaveOccurred())

			// d1 is online [0,36h): 12h of morning exposure, 12h of evening
			Expect(findPanelRow(analysis.Panel, "d1", "morning").Exposure).To(BeNumerically("==", 43200))
			Expect(findPanelRow(analysis.Panel, "d1", "evening").Exposure).To(BeNumerically("==", 43200))
			Expect(findPanelRow(analysis.Panel, "d1", "evening").Events).To(Equal(1))
			Expect(analysis.Exclusions).To(Equal([]Exclusion{
				{EventID: "in-gap", DeviceID: "d1", Time: hoursAfter(18), Reason: ExcludedOutsidePeriods},
			}))
		})

		It("auto-names unnamed periods", func() {
			spec.Periods = []Period{{Start: day0, End: hoursAfter(24)}}
			analysis, err := AnalyzeDeviceRates(canonicalSnapshot(), spec)
			Expect(err).NotTo(HaveOccurred())
			Expect(analysis.Periods[0].Name).To(Equal("[2026-01-01T00:00:00Z, 2026-01-02T00:00:00Z)"))
		})

		It("fits an intercept-only model on the exposed rows", func() {
			analysis, err := AnalyzeDeviceRates(canonicalSnapshot(), spec)
			Expect(err).NotTo(HaveOccurred())
			Expect(analysis.ModelRows).To(HaveLen(5))
			for _, row := range analysis.ModelRows {
				Expect(row.Exposure).To(BeNumerically(">", 0))
				Expect(row.Offset).To(BeNumerically("~", math.Log(row.Exposure), 1e-12))
			}
			// the intercept-only score equation makes fitted counts sum to observed
			total := 0.0
			for _, row := range analysis.ModelRows {
				total += row.Predicted
			}
			Expect(total).To(BeNumerically("~", 6, 1e-6))
			Expect(analysis.Model.Coefficients).To(HaveLen(1))
			Expect(analysis.Model.Coefficients[0].Name).To(Equal("(Intercept)"))
			Expect(analysis.Model.Coefficients[0].Estimate).To(BeNumerically("~", -10.9609, 1e-3)) // log(6/345600)
		})
	})

	Describe("grouped rate report", func() {
		BeforeEach(func() {
			spec.Covariates = []Covariate{{Name: "model", Type: Categorical}}
		})

		It("pools events and exposure per group instead of averaging row rates", func() {
			analysis, err := AnalyzeDeviceRates(canonicalSnapshot(), spec)
			Expect(err).NotTo(HaveOccurred())
			Expect(analysis.GroupRates).To(HaveLen(2))

			groupA := analysis.GroupRates[0]
			Expect(groupA.Covariates).To(Equal(map[string]string{"model": "A"}))
			Expect(groupA.Devices).To(Equal(2))
			Expect(groupA.Exposure).To(BeNumerically("==", 216000))
			Expect(groupA.Events).To(Equal(5))
			Expect(*groupA.Rate).To(BeNumerically("~", 5.0/216000, 1e-12))
			// the mean of the per-row rates would be ~2.7e-5, not the pooled rate
			Expect(*groupA.Rate).NotTo(BeNumerically("~", 2.7e-5, 1e-7))

			groupB := analysis.GroupRates[1]
			Expect(groupB.Covariates).To(Equal(map[string]string{"model": "B"}))
			Expect(groupB.Devices).To(Equal(2)) // includes the zero-exposure device
			Expect(*groupB.Rate).To(BeNumerically("~", 1.0/129600, 1e-12))
		})

		It("reports a nil rate for a group with no exposure", func() {
			spec.Covariates = append(spec.Covariates, Covariate{Name: "region", Type: Categorical})
			analysis, err := AnalyzeDeviceRates(canonicalSnapshot(), spec)
			Expect(err).NotTo(HaveOccurred())
			Expect(analysis.GroupRates).To(HaveLen(4))
			for _, group := range analysis.GroupRates {
				if group.Covariates["model"] == "B" && group.Covariates["region"] == "US" {
					Expect(group.Exposure).To(BeNumerically("==", 0))
					Expect(group.Rate).To(BeNil())
					return
				}
			}
			Fail("no group found for model=B, region=US")
		})
	})

	Describe("input validation", func() {
		It("fails on a nil snapshot", func() {
			_, err := AnalyzeDeviceRates(nil, spec)
			expectAnalysisFailure(err, SpecError, "snapshot")
		})

		It("fails on an empty population", func() {
			_, err := AnalyzeDeviceRates(&DeviceSnapshot{}, spec)
			expectAnalysisFailure(err, PopulationError, "no devices")
		})

		It("fails on intervals referencing unknown devices", func() {
			snapshot := canonicalSnapshot()
			snapshot.Online = append(snapshot.Online, Interval{DeviceID: "ghost", Start: day0, End: hoursAfter(1)})
			_, err := AnalyzeDeviceRates(snapshot, spec)
			expectAnalysisFailure(err, PopulationError, "ghost")
		})

		It("fails on events referencing unknown devices", func() {
			snapshot := canonicalSnapshot()
			snapshot.Events = append(snapshot.Events, DeviceEvent{ID: "ex", DeviceID: "ghost", Time: hoursAfter(1)})
			_, err := AnalyzeDeviceRates(snapshot, spec)
			expectAnalysisFailure(err, PopulationError, "ghost")
		})

		It("fails on conflicting duplicate device attributes", func() {
			snapshot := canonicalSnapshot()
			snapshot.Devices = append(snapshot.Devices, Device{ID: "d1", Covariates: map[string]string{"model": "Z", "region": "EU"}})
			_, err := AnalyzeDeviceRates(snapshot, spec)
			expectAnalysisFailure(err, ConflictError, "d1")
		})

		It("accepts identical duplicate device declarations", func() {
			snapshot := canonicalSnapshot()
			snapshot.Devices = append(snapshot.Devices, Device{ID: "d1", Covariates: map[string]string{"model": "A", "region": "EU"}})
			analysis, err := AnalyzeDeviceRates(snapshot, spec)
			Expect(err).NotTo(HaveOccurred())
			Expect(analysis.Panel).To(HaveLen(8))
		})

		It("fails on conflicting duplicate events", func() {
			snapshot := canonicalSnapshot()
			snapshot.Events = append(snapshot.Events, DeviceEvent{ID: "e3", DeviceID: "d1", Time: hoursAfter(26)})
			_, err := AnalyzeDeviceRates(snapshot, spec)
			expectAnalysisFailure(err, ConflictError, "e3")
		})

		It("fails on intervals with non-positive duration", func() {
			snapshot := canonicalSnapshot()
			snapshot.Online = append(snapshot.Online, Interval{DeviceID: "d1", Start: hoursAfter(2), End: hoursAfter(2)})
			_, err := AnalyzeDeviceRates(snapshot, spec)
			expectAnalysisFailure(err, TypeError, "non-positive duration")
		})

		It("fails on events with an empty stable ID", func() {
			snapshot := canonicalSnapshot()
			snapshot.Events = append(snapshot.Events, DeviceEvent{DeviceID: "d1", Time: hoursAfter(1)})
			_, err := AnalyzeDeviceRates(snapshot, spec)
			expectAnalysisFailure(err, TypeError, "stable ID")
		})

		It("fails when a device is missing a declared covariate", func() {
			spec.Covariates = []Covariate{{Name: "missing", Type: Categorical}}
			_, err := AnalyzeDeviceRates(canonicalSnapshot(), spec)
			expectAnalysisFailure(err, TypeError, "missing")
		})

		It("fails when a numeric covariate has a non-numeric value", func() {
			snapshot := canonicalSnapshot()
			snapshot.Devices[0].Covariates["load"] = "high"
			spec.Covariates = []Covariate{{Name: "load", Type: Numeric}}
			_, err := AnalyzeDeviceRates(snapshot, spec)
			expectAnalysisFailure(err, TypeError, "load")
		})

		It("fails on undeclared period specifications", func() {
			_, err := AnalyzeDeviceRates(canonicalSnapshot(), AnalysisSpec{})
			expectAnalysisFailure(err, SpecError, "period")
		})

		It("fails on overlapping periods", func() {
			spec.Periods = []Period{
				{Name: "a", Start: day0, End: hoursAfter(30)},
				{Name: "b", Start: hoursAfter(24), End: hoursAfter(48)},
			}
			_, err := AnalyzeDeviceRates(canonicalSnapshot(), spec)
			expectAnalysisFailure(err, SpecError, "overlap")
		})

		It("fails on a period with non-positive duration", func() {
			spec.Periods = []Period{{Name: "bad", Start: hoursAfter(24), End: hoursAfter(24)}}
			_, err := AnalyzeDeviceRates(canonicalSnapshot(), spec)
			expectAnalysisFailure(err, SpecError, "non-positive")
		})

		It("fails on duplicate period names", func() {
			spec.Periods = []Period{
				{Name: "same", Start: day0, End: hoursAfter(24)},
				{Name: "same", Start: hoursAfter(24), End: hoursAfter(48)},
			}
			_, err := AnalyzeDeviceRates(canonicalSnapshot(), spec)
			expectAnalysisFailure(err, SpecError, "duplicate")
		})

		It("fails on an unknown covariate type", func() {
			spec.Covariates = []Covariate{{Name: "model", Type: "ordinal"}}
			_, err := AnalyzeDeviceRates(canonicalSnapshot(), spec)
			expectAnalysisFailure(err, SpecError, "unknown type")
		})
	})
})
