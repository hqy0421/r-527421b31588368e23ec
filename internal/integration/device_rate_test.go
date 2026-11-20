package integration_test

import (
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"github.com/sclevine/agouti"
)

var _ = Describe("Device rate analysis", func() {
	var (
		page   *agouti.Page
		server *httptest.Server
	)

	BeforeEach(func() {
		server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			html, _ := ioutil.ReadFile("device_rate_test_page.html")
			response.Write(html)
		}))

		var err error
		page, err = phantomDriver.NewPage()
		Expect(err).NotTo(HaveOccurred())
		Expect(page.Navigate(server.URL)).To(Succeed())
	})

	AfterEach(func() {
		Expect(page.Destroy()).To(Succeed())
		server.Close()
	})

	It("should read the snapshot from the page and analyze it", func() {
		spec := agouti.AnalysisSpec{
			Periods: []agouti.Period{
				{Name: "day1", Start: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
				{Name: "day2", Start: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)},
			},
			Covariates: []agouti.Covariate{{Name: "model", Type: agouti.Categorical}},
		}

		analysis, err := page.AnalyzeDeviceRates(agouti.SnapshotPageConfig{}, spec)
		Expect(err).NotTo(HaveOccurred())

		Expect(analysis.Version).To(Equal("2026.1"))
		Expect(analysis.Panel).To(HaveLen(4))

		By("unifying the +02:00 online interval to UTC and clipping it to day1")
		row := findAnalysisPanelRow(analysis.Panel, "d1", "day1")
		Expect(row.Exposure).To(BeNumerically("==", 43200))
		Expect(row.Events).To(Equal(1))

		By("deducting maintenance from d2's day2 exposure")
		row = findAnalysisPanelRow(analysis.Panel, "d2", "day2")
		Expect(row.Exposure).To(BeNumerically("==", 64800))
		Expect(row.Events).To(Equal(1))

		By("excluding the event that fired under maintenance")
		Expect(analysis.Exclusions).To(HaveLen(1))
		Expect(analysis.Exclusions[0].EventID).To(Equal("e3"))
		Expect(analysis.Exclusions[0].Reason).To(Equal(agouti.ExcludedMaintenance))

		By("fitting the Poisson model on the exposed rows")
		Expect(analysis.ModelRows).To(HaveLen(2))
		Expect(analysis.Model.Coefficients).To(HaveLen(2))
		Expect(analysis.ModelRows[0].Predicted).To(BeNumerically("~", 1, 1e-6))

		By("reporting pooled group rates")
		Expect(analysis.GroupRates).To(HaveLen(2))
		for _, group := range analysis.GroupRates {
			Expect(group.Events).To(Equal(1))
			Expect(group.Rate).NotTo(BeNil())
			Expect(*group.Rate).To(BeNumerically("~", 1.0/group.Exposure, 1e-12))
		}
	})
})

func findAnalysisPanelRow(panel []agouti.PanelRow, deviceID, period string) agouti.PanelRow {
	for _, row := range panel {
		if row.DeviceID == deviceID && row.Period == period {
			return row
		}
	}
	Fail("panel row not found: " + deviceID + " / " + period)
	return agouti.PanelRow{}
}
