package agouti_test

import (
	"errors"
	"strings"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	. "github.com/sclevine/agouti"
	"github.com/sclevine/agouti/internal/mocks"
)

const snapshotPageJSON = `{
	"tables": {
		"devices": {"version": "v9", "complete": "true", "rows": [
			{"device-id": "d1", "model": "A"},
			{"device-id": "d2", "model": "B"}
		]},
		"online": {"version": "v9", "complete": "true", "rows": [
			{"device-id": "d1", "start": "2026-01-01T02:00:00+02:00", "end": "2026-01-01T14:00:00+02:00"},
			{"device-id": "d2", "start": "2026-01-02 00:00:00", "end": "2026-01-03 00:00:00"}
		]},
		"maintenance": {"version": "v9", "complete": "true", "rows": [
			{"device-id": "d2", "start": "2026-01-02T06:00:00Z", "end": "2026-01-02T12:00:00Z"}
		]},
		"events": {"version": "v9", "complete": "true", "rows": [
			{"id": "e1", "device-id": "d1", "time": "2026-01-01T05:00:00Z", "kind": "alarm"},
			{"id": "e2", "device-id": "d2", "time": "2026-01-02T13:00:00Z", "kind": "alarm"},
			{"id": "e3", "device-id": "d2", "time": "2026-01-02T08:00:00Z", "kind": "alarm"},
			{"id": "e4", "device-id": "d1", "time": "2026-01-01T20:00:00Z", "kind": "alarm"}
		]}
	}
}`

var _ = Describe("Page device snapshot reading", func() {
	var (
		page    *Page
		session *mocks.Session
	)

	BeforeEach(func() {
		session = &mocks.Session{}
		page = NewTestPage(session)
		session.ExecuteCall.Result = snapshotPageJSON
	})

	Describe("ReadDeviceSnapshot", func() {
		It("reads devices, intervals, and events of the completed version from the page", func() {
			snapshot, err := page.ReadDeviceSnapshot(SnapshotPageConfig{})
			Expect(err).NotTo(HaveOccurred())

			Expect(snapshot.Version).To(Equal("v9"))
			Expect(snapshot.Devices).To(HaveLen(2))
			Expect(snapshot.Devices[0].ID).To(Equal("d1"))
			Expect(snapshot.Devices[0].Covariates).To(Equal(map[string]string{"model": "A"}))

			// the +02:00 interval is unified to UTC
			Expect(snapshot.Online[0].Start).To(BeTemporally("==", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)))
			Expect(snapshot.Online[0].End).To(BeTemporally("==", time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)))
			// naive timestamps are read as UTC
			Expect(snapshot.Online[1].Start).To(BeTemporally("==", time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)))

			Expect(snapshot.Maintenance).To(HaveLen(1))
			Expect(snapshot.Events).To(HaveLen(4))
			Expect(snapshot.Events[0].ID).To(Equal("e1"))
			Expect(snapshot.Events[0].Kind).To(Equal("alarm"))
			Expect(snapshot.Events[0].Time).To(BeTemporally("==", time.Date(2026, 1, 1, 5, 0, 0, 0, time.UTC)))
		})

		It("extracts all four tables in one script call using the default selectors", func() {
			_, err := page.ReadDeviceSnapshot(SnapshotPageConfig{})
			Expect(err).NotTo(HaveOccurred())
			Expect(session.ExecuteCall.Arguments).To(HaveLen(1))
			Expect(session.ExecuteCall.Arguments[0]).To(Equal(map[string]string{
				"devices":     "#devices",
				"online":      "#online-intervals",
				"maintenance": "#maintenance-intervals",
				"events":      "#events",
			}))
			Expect(session.ExecuteCall.Body).To(ContainSubstring("data-version"))
			Expect(session.ExecuteCall.Body).To(ContainSubstring("data-complete"))
			Expect(session.ExecuteCall.Body).To(ContainSubstring("data-field"))
		})

		It("honors custom table selectors", func() {
			_, err := page.ReadDeviceSnapshot(SnapshotPageConfig{DevicesTable: "#fleet", EventsTable: "#alarms"})
			Expect(err).NotTo(HaveOccurred())
			selectors := session.ExecuteCall.Arguments[0].(map[string]string)
			Expect(selectors["devices"]).To(Equal("#fleet"))
			Expect(selectors["events"]).To(Equal("#alarms"))
			Expect(selectors["online"]).To(Equal("#online-intervals"))
		})

		It("fails when the page script fails", func() {
			session.ExecuteCall.Err = errors.New("webdriver is down")
			_, err := page.ReadDeviceSnapshot(SnapshotPageConfig{})
			Expect(err).To(MatchError(ContainSubstring("failed to read device snapshot from page: failed to run script: webdriver is down")))
		})

		It("fails when a table is missing from the page", func() {
			session.ExecuteCall.Result = `{"error": "no element found for selector: #devices"}`
			_, err := page.ReadDeviceSnapshot(SnapshotPageConfig{})
			Expect(err).To(MatchError(ContainSubstring("no element found for selector: #devices")))
		})

		It("fails when the extracted snapshot is missing a table", func() {
			session.ExecuteCall.Result = `{"tables": {"devices": {"version": "v9", "complete": "true", "rows": []}}}`
			_, err := page.ReadDeviceSnapshot(SnapshotPageConfig{})
			Expect(err).To(MatchError(ContainSubstring(`missing the "online" table`)))
		})

		It("fails when the tables belong to different versions", func() {
			session.ExecuteCall.Result = strings.Replace(snapshotPageJSON,
				`"events": {"version": "v9"`, `"events": {"version": "v8"`, 1)
			_, err := page.ReadDeviceSnapshot(SnapshotPageConfig{})
			expectAnalysisFailure(err, VersionError, "different snapshot versions")
		})

		It("fails when a table is not marked complete", func() {
			session.ExecuteCall.Result = strings.Replace(snapshotPageJSON,
				`"maintenance": {"version": "v9", "complete": "true"`, `"maintenance": {"version": "v9", "complete": "false"`, 1)
			_, err := page.ReadDeviceSnapshot(SnapshotPageConfig{})
			expectAnalysisFailure(err, VersionError, "not marked complete")
		})

		It("fails when a table does not declare a version", func() {
			session.ExecuteCall.Result = strings.Replace(snapshotPageJSON,
				`"devices": {"version": "v9", "complete": "true"`, `"devices": {"complete": "true"`, 1)
			_, err := page.ReadDeviceSnapshot(SnapshotPageConfig{})
			expectAnalysisFailure(err, VersionError, "does not declare a snapshot version")
		})

		It("fails on an unparseable timestamp", func() {
			session.ExecuteCall.Result = strings.Replace(snapshotPageJSON,
				`"start": "2026-01-01T02:00:00+02:00"`, `"start": "soon"`, 1)
			_, err := page.ReadDeviceSnapshot(SnapshotPageConfig{})
			expectAnalysisFailure(err, TypeError, "invalid start")
		})

		It("fails on a device row without a device-id", func() {
			session.ExecuteCall.Result = strings.Replace(snapshotPageJSON,
				`{"device-id": "d1", "model": "A"}`, `{"model": "A"}`, 1)
			_, err := page.ReadDeviceSnapshot(SnapshotPageConfig{})
			expectAnalysisFailure(err, TypeError, "missing a device-id")
		})

		It("fails on an event row without a stable id", func() {
			session.ExecuteCall.Result = strings.Replace(snapshotPageJSON,
				`{"id": "e1", "device-id": "d1"`, `{"device-id": "d1"`, 1)
			_, err := page.ReadDeviceSnapshot(SnapshotPageConfig{})
			expectAnalysisFailure(err, TypeError, "stable event id")
		})
	})

	Describe("AnalyzeDeviceRates", func() {
		It("reads the page and returns the full analysis in one call", func() {
			spec := AnalysisSpec{
				Periods: []Period{
					{Name: "day1", Start: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
					{Name: "day2", Start: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)},
				},
				Covariates: []Covariate{{Name: "model", Type: Categorical}},
			}
			analysis, err := page.AnalyzeDeviceRates(SnapshotPageConfig{}, spec)
			Expect(err).NotTo(HaveOccurred())

			Expect(analysis.Version).To(Equal("v9"))
			Expect(analysis.Panel).To(HaveLen(4)) // 2 devices x 2 periods

			// d1: online [00:00, 12:00) UTC on day1 only
			Expect(findPanelRow(analysis.Panel, "d1", "day1").Exposure).To(BeNumerically("==", 43200))
			Expect(findPanelRow(analysis.Panel, "d1", "day1").Events).To(Equal(1))
			Expect(findPanelRow(analysis.Panel, "d1", "day2").Rate).To(BeNil())

			// d2: 24h online on day2 minus 6h of maintenance
			Expect(findPanelRow(analysis.Panel, "d2", "day2").Exposure).To(BeNumerically("==", 64800))
			Expect(findPanelRow(analysis.Panel, "d2", "day2").Events).To(Equal(1))

			// e4 fired while d1 was offline, e3 during d2's maintenance;
			// exclusions are listed in event-time order
			Expect(analysis.Exclusions).To(Equal([]Exclusion{
				{EventID: "e4", DeviceID: "d1", Time: time.Date(2026, 1, 1, 20, 0, 0, 0, time.UTC), Reason: ExcludedOffline},
				{EventID: "e3", DeviceID: "d2", Time: time.Date(2026, 1, 2, 8, 0, 0, 0, time.UTC), Reason: ExcludedMaintenance},
			}))

			Expect(analysis.ModelRows).To(HaveLen(2))
			Expect(analysis.Model.Coefficients).To(HaveLen(2)) // intercept + model=B
			Expect(analysis.GroupRates).To(HaveLen(2))
			for _, group := range analysis.GroupRates {
				Expect(group.Events).To(Equal(1))
				Expect(group.Rate).NotTo(BeNil())
			}
		})
	})
})
