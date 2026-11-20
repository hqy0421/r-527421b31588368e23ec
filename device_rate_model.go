package agouti

import (
	"math"
	"sort"
	"strconv"
)

const (
	maxIRLSIterations  = 100
	convergenceEpsilon = 1e-9
)

// fitPoissonModel fits a Poisson regression with a log link to the model
// rows, using each row's Offset (the log of its effective exposure), and
// writes the predicted event counts back into the rows. The design matrix
// holds an intercept, one dummy per non-reference level of each declared
// categorical covariate, and one column per numeric covariate.
//
// The fit fails with a NotEstimableError when no events were observed,
// when the design matrix is rank deficient, or when the iteratively
// reweighted least squares fit does not converge (for example because a
// covariate level has exposure but no events, which separates the data).
func fitPoissonModel(rows []ModelRow, covariates []Covariate) (*PoissonFit, error) {
	n := len(rows)
	y := make([]float64, n)
	offset := make([]float64, n)
	total := 0.0
	for i, row := range rows {
		y[i] = float64(row.Events)
		offset[i] = row.Offset
		total += y[i]
	}
	if total == 0 {
		return nil, newAnalysisError(NotEstimableError, "no events observed in any exposed device-period; coefficients would not be finite")
	}
	x, names, err := designMatrix(rows, covariates)
	if err != nil {
		return nil, err
	}
	p := len(names)
	if p > n {
		return nil, newAnalysisError(NotEstimableError, "only %d exposed rows for %d coefficients", n, p)
	}

	beta := make([]float64, p)
	var sumMu float64
	for i := 0; i < n; i++ {
		sumMu += math.Exp(offset[i])
	}
	beta[0] = math.Log(total / sumMu)

	var converged bool
	var iterations int
	for iterations = 1; iterations <= maxIRLSIterations; iterations++ {
		xtwx, xtwz := weightedNormalEquations(x, y, offset, beta)
		next, ok := solveLinear(xtwx, xtwz)
		if !ok {
			return nil, newAnalysisError(NotEstimableError, "design matrix is rank deficient (collinear covariates)")
		}
		delta := 0.0
		for j := range beta {
			if d := math.Abs(next[j] - beta[j]); d > delta {
				delta = d
			}
		}
		beta = next
		if diverging(beta) {
			return nil, newAnalysisError(NotEstimableError, "iterative fit did not converge (diverging coefficients suggest separation: a covariate level may have no events)")
		}
		if delta < convergenceEpsilon {
			converged = true
			break
		}
	}
	if !converged {
		return nil, newAnalysisError(NotEstimableError, "iterative fit did not converge after %d iterations (possible separation: a covariate level may have no events)", maxIRLSIterations)
	}

	predicted := make([]float64, n)
	for i := 0; i < n; i++ {
		predicted[i] = math.Exp(eta(x[i], beta, offset[i]))
		rows[i].Predicted = predicted[i]
	}

	xtwx, _ := weightedNormalEquations(x, y, offset, beta)
	stdErr := make([]float64, p)
	invertible := true
	for j := 0; j < p; j++ {
		unit := make([]float64, p)
		unit[j] = 1
		column, ok := solveLinear(xtwx, unit)
		if !ok {
			invertible = false
			break
		}
		stdErr[j] = math.Sqrt(math.Abs(column[j]))
	}

	coefficients := make([]Coefficient, p)
	for j := 0; j < p; j++ {
		se := math.NaN()
		if invertible {
			se = stdErr[j]
		}
		coefficients[j] = Coefficient{Name: names[j], Estimate: beta[j], StdErr: se}
	}

	deviance := 0.0
	for i := 0; i < n; i++ {
		if y[i] > 0 {
			deviance += y[i]*math.Log(y[i]/predicted[i]) - (y[i] - predicted[i])
		} else {
			deviance += predicted[i]
		}
	}

	return &PoissonFit{
		Coefficients:     coefficients,
		Iterations:       iterations,
		Deviance:         2 * deviance,
		DegreesOfFreedom: n - p,
	}, nil
}

// designMatrix builds the model matrix and the coefficient names for the
// declared covariates. Categorical levels are collected from the model
// rows themselves; the alphabetically first level is the reference.
func designMatrix(rows []ModelRow, covariates []Covariate) ([][]float64, []string, error) {
	names := []string{"(Intercept)"}
	type categoricalSpec struct {
		name   string
		levels []string
	}
	var categorical []categoricalSpec
	var numeric []string
	for _, covariate := range covariates {
		switch covariate.Type {
		case Categorical:
			seen := map[string]bool{}
			for _, row := range rows {
				seen[row.Covariates[covariate.Name]] = true
			}
			var levels []string
			for level := range seen {
				levels = append(levels, level)
			}
			sort.Strings(levels)
			categorical = append(categorical, categoricalSpec{covariate.Name, levels})
			for _, level := range levels[1:] {
				names = append(names, covariate.Name+"="+level)
			}
		case Numeric:
			numeric = append(numeric, covariate.Name)
			names = append(names, covariate.Name)
		default:
			return nil, nil, newAnalysisError(SpecError, "covariate %q has unknown type %q", covariate.Name, covariate.Type)
		}
	}
	x := make([][]float64, len(rows))
	for i, row := range rows {
		values := []float64{1}
		for _, spec := range categorical {
			for _, level := range spec.levels[1:] {
				if row.Covariates[spec.name] == level {
					values = append(values, 1)
				} else {
					values = append(values, 0)
				}
			}
		}
		for _, name := range numeric {
			value, err := strconv.ParseFloat(row.Covariates[name], 64)
			if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
				return nil, nil, newAnalysisError(TypeError, "device %q has non-numeric value %q for numeric covariate %q", row.DeviceID, row.Covariates[name], name)
			}
			values = append(values, value)
		}
		x[i] = values
	}
	return x, names, nil
}

// weightedNormalEquations accumulates X'WX and X'Wz for one IRLS step.
// For a Poisson log link the weight is mu and Wz simplifies to
// mu*(eta-offset) + (y-mu), which avoids dividing by a vanishing mu.
func weightedNormalEquations(x [][]float64, y, offset, beta []float64) ([][]float64, []float64) {
	p := len(beta)
	xtwx := make([][]float64, p)
	for j := range xtwx {
		xtwx[j] = make([]float64, p)
	}
	xtwz := make([]float64, p)
	for i := range x {
		etaValue := eta(x[i], beta, offset[i])
		mu := math.Exp(etaValue)
		wz := mu*(etaValue-offset[i]) + (y[i] - mu)
		for j := 0; j < p; j++ {
			xtwz[j] += x[i][j] * wz
			for k := j; k < p; k++ {
				xtwx[j][k] += mu * x[i][j] * x[i][k]
			}
		}
	}
	for j := 0; j < p; j++ {
		for k := 0; k < j; k++ {
			xtwx[j][k] = xtwx[k][j]
		}
	}
	return xtwx, xtwz
}

// eta computes the clamped linear predictor X*beta + offset. The clamp
// only binds when the fit is diverging, in which case the iteration fails
// to converge and the model is reported as not estimable.
func eta(row, beta []float64, offset float64) float64 {
	value := offset
	for j, x := range row {
		value += x * beta[j]
	}
	return math.Max(math.Min(value, 100), -100)
}

// diverging reports whether any coefficient has grown past the point of a
// plausible log event rate, which indicates separation rather than a
// finite estimate. It is checked before the vanishing weights can make
// the normal equations numerically singular.
func diverging(beta []float64) bool {
	for _, b := range beta {
		if math.Abs(b) > 25 {
			return true
		}
	}
	return false
}

// solveLinear solves A x = b by Gauss-Jordan elimination with partial
// pivoting. It reports failure when the matrix is rank deficient.
func solveLinear(a [][]float64, b []float64) ([]float64, bool) {
	n := len(a)
	m := make([][]float64, n)
	maxAbs := 0.0
	for i := range m {
		row := make([]float64, n+1)
		copy(row, a[i])
		row[n] = b[i]
		for _, value := range a[i] {
			if math.Abs(value) > maxAbs {
				maxAbs = math.Abs(value)
			}
		}
		m[i] = row
	}
	threshold := maxAbs * 1e-12
	for column := 0; column < n; column++ {
		pivot := column
		for r := column + 1; r < n; r++ {
			if math.Abs(m[r][column]) > math.Abs(m[pivot][column]) {
				pivot = r
			}
		}
		if math.Abs(m[pivot][column]) <= threshold {
			return nil, false
		}
		m[column], m[pivot] = m[pivot], m[column]
		for r := 0; r < n; r++ {
			if r == column {
				continue
			}
			factor := m[r][column] / m[column][column]
			if factor == 0 {
				continue
			}
			for c := column; c <= n; c++ {
				m[r][c] -= factor * m[column][c]
			}
		}
	}
	x := make([]float64, n)
	for i := 0; i < n; i++ {
		x[i] = m[i][n] / m[i][i]
	}
	return x, true
}

func logOffset(exposure float64) float64 {
	return math.Log(exposure)
}

func isFiniteFloat(value string) bool {
	parsed, err := strconv.ParseFloat(value, 64)
	return err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0)
}
