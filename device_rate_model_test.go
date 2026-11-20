package agouti_test

import (
	"fmt"
	"math"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	. "github.com/sclevine/agouti"
)

// modelSnapshot builds devices that are each online for exactly 1000
// seconds inside a single 2000-second period, with the given covariate
// values and event counts.
func modelSnapshot(covariate string, values []string, counts []int) *DeviceSnapshot {
	snapshot := &DeviceSnapshot{Version: "v1"}
	for i, value := range values {
		id := fmt.Sprintf("d%d", i)
		snapshot.Devices = append(snapshot.Devices, Device{ID: id, Covariates: map[string]string{covariate: value}})
		snapshot.Online = append(snapshot.Online, Interval{DeviceID: id, Start: day0, End: day0.Add(1000 * time.Second)})
		for j := 0; j < counts[i]; j++ {
			snapshot.Events = append(snapshot.Events, DeviceEvent{
				ID:       fmt.Sprintf("e-%d-%d", i, j),
				DeviceID: id,
				Time:     day0.Add(time.Duration(j+1) * time.Second),
			})
		}
	}
	return snapshot
}

func modelSpec(covariates ...Covariate) AnalysisSpec {
	return AnalysisSpec{
		Periods:    []Period{{Name: "p", Start: day0, End: day0.Add(2000 * time.Second)}},
		Covariates: covariates,
	}
}

func coefficient(fit *PoissonFit, name string) Coefficient {
	for _, c := range fit.Coefficients {
		if c.Name == name {
			return c
		}
	}
	Fail("coefficient not found: " + name)
	return Coefficient{}
}

var _ = Describe("Poisson model fitting", func() {
	Describe("coefficient recovery", func() {
		It("recovers exact group rates for a categorical covariate", func() {
			snapshot := modelSnapshot("model", []string{"A", "B"}, []int{10, 40})
			analysis, err := AnalyzeDeviceRates(snapshot, modelSpec(Covariate{Name: "model", Type: Categorical}))
			Expect(err).NotTo(HaveOccurred())

			intercept := coefficient(analysis.Model, "(Intercept)")
			Expect(intercept.Estimate).To(BeNumerically("~", math.Log(0.01), 1e-6))
			Expect(intercept.StdErr).To(BeNumerically("~", 1.0/math.Sqrt(10), 1e-6))

			slope := coefficient(analysis.Model, "model=B")
			Expect(slope.Estimate).To(BeNumerically("~", math.Log(4), 1e-6))
			Expect(slope.StdErr).To(BeNumerically("~", math.Sqrt(1.0/10+1.0/40), 1e-6))

			Expect(analysis.ModelRows[0].Predicted).To(BeNumerically("~", 10, 1e-6))
			Expect(analysis.ModelRows[1].Predicted).To(BeNumerically("~", 40, 1e-6))
			Expect(analysis.Model.DegreesOfFreedom).To(Equal(0))
		})

		It("recovers the exact slope for a numeric covariate", func() {
			snapshot := modelSnapshot("load", []string{"0", "1", "2"}, []int{5, 10, 20})
			analysis, err := AnalyzeDeviceRates(snapshot, modelSpec(Covariate{Name: "load", Type: Numeric}))
			Expect(err).NotTo(HaveOccurred())

			Expect(coefficient(analysis.Model, "(Intercept)").Estimate).To(BeNumerically("~", math.Log(0.005), 1e-6))
			Expect(coefficient(analysis.Model, "load").Estimate).To(BeNumerically("~", math.Log(2), 1e-6))

			for i, expected := range []float64{5, 10, 20} {
				Expect(analysis.ModelRows[i].Predicted).To(BeNumerically("~", expected, 1e-6))
			}
		})

		It("uses the log of effective exposure as offset", func() {
			snapshot := modelSnapshot("model", []string{"A", "A"}, []int{5, 20})
			analysis, err := AnalyzeDeviceRates(snapshot, modelSpec())
			Expect(err).NotTo(HaveOccurred())
			Expect(analysis.ModelRows).To(HaveLen(2))
			for _, row := range analysis.ModelRows {
				Expect(row.Offset).To(BeNumerically("~", math.Log(1000), 1e-12))
			}
			// intercept-only: pooled rate 25/2000 events per second
			Expect(coefficient(analysis.Model, "(Intercept)").Estimate).To(BeNumerically("~", math.Log(25.0/2000), 1e-6))
			Expect(analysis.ModelRows[0].Predicted).To(BeNumerically("~", 12.5, 1e-6))
		})

		It("satisfies the score equations for a mixed categorical and numeric model", func() {
			snapshot := modelSnapshot("model", []string{"A", "A", "B", "B"}, []int{3, 6, 9, 14})
			for i, load := range []string{"0.5", "1.5", "0.5", "1.5"} {
				snapshot.Devices[i].Covariates["load"] = load
			}
			spec := modelSpec(
				Covariate{Name: "model", Type: Categorical},
				Covariate{Name: "load", Type: Numeric},
			)
			analysis, err := AnalyzeDeviceRates(snapshot, spec)
			Expect(err).NotTo(HaveOccurred())
			Expect(analysis.Model.Coefficients).To(HaveLen(3))

			// at convergence, fitted counts sum to observed counts per level
			totals := map[string][2]float64{}
			for _, row := range analysis.ModelRows {
				total := totals[row.Covariates["model"]]
				total[0] += float64(row.Events)
				total[1] += row.Predicted
				totals[row.Covariates["model"]] = total
			}
			for level, total := range totals {
				Expect(total[1]).To(BeNumerically("~", total[0], 1e-6), "level "+level)
			}
		})
	})

	Describe("estimability failures", func() {
		It("fails when no events were observed", func() {
			snapshot := modelSnapshot("model", []string{"A", "B"}, []int{0, 0})
			_, err := AnalyzeDeviceRates(snapshot, modelSpec())
			expectAnalysisFailure(err, NotEstimableError, "no events")
		})

		It("fails when no row has positive exposure", func() {
			snapshot := &DeviceSnapshot{
				Version: "v1",
				Devices: []Device{{ID: "d0", Covariates: map[string]string{}}},
			}
			_, err := AnalyzeDeviceRates(snapshot, modelSpec())
			expectAnalysisFailure(err, NotEstimableError, "positive exposure")
		})

		It("fails on collinear numeric covariates", func() {
			snapshot := modelSnapshot("x1", []string{"0", "1", "2"}, []int{5, 10, 20})
			for i := range snapshot.Devices {
				snapshot.Devices[i].Covariates["x2"] = []string{"0", "2", "4"}[i]
			}
			spec := modelSpec(
				Covariate{Name: "x1", Type: Numeric},
				Covariate{Name: "x2", Type: Numeric},
			)
			_, err := AnalyzeDeviceRates(snapshot, spec)
			expectAnalysisFailure(err, NotEstimableError, "rank deficient")
		})

		It("fails when a covariate level has exposure but no events", func() {
			snapshot := modelSnapshot("model", []string{"A", "B"}, []int{10, 0})
			_, err := AnalyzeDeviceRates(snapshot, modelSpec(Covariate{Name: "model", Type: Categorical}))
			expectAnalysisFailure(err, NotEstimableError, "did not converge")
		})
	})
})
