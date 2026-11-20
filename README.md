Agouti
======

[![Build Status](https://api.travis-ci.org/sclevine/agouti.png?branch=master)](http://travis-ci.org/sclevine/agouti)
[![GoDoc](https://godoc.org/github.com/sclevine/agouti?status.svg)](https://godoc.org/github.com/sclevine/agouti)

**NOTE: Agouti is no longer actively maintained. This was one of my earliest Go projects, and I am no longer happy with the API design. I recommend selecting an alternative WebDriver client for Go.**

Agouti is a library for writing browser-based acceptance tests in Google Go. It provides [Gomega](https://github.com/onsi/gomega) matchers and plays nicely with [Ginkgo](https://github.com/onsi/ginkgo) or [Spec](https://github.com/sclevine/spec). See [agouti.org](http://agouti.org) and the [GoDoc](https://godoc.org/github.com/sclevine/agouti) for documentation. Have questions? Check out the [Agouti mailing list](https://groups.google.com/d/forum/agouti) or the #agouti IRC channel on Freenode.

The [integration tests](https://github.com/sclevine/agouti/blob/master/internal/integration/) are a great place to see everything in action and get started quickly!

Device rate analysis
--------------------

Device alarm counts are not failure rates. Agouti can turn the telemetry
shown on a logged-in page into proper event rates per unit of exposure.
`page.ReadDeviceSnapshot` reads the device attributes, online intervals,
maintenance intervals, and stable-ID events of one completed snapshot
version from four HTML tables, and `AnalyzeDeviceRates`:

* merges overlapping online intervals at unified UTC instants and deducts
  maintenance,
* splits the effective exposure over declared half-open statistical
  periods into a complete device x period panel (cells without exposure
  keep a nil rate instead of a fabricated zero),
* counts each event once by stable ID and lists events excluded for
  firing offline, under maintenance, or outside every declared period,
* fits a Poisson regression on the declared covariates with the log of
  effective exposure as offset, and reports the actual model rows,
  coefficients, predicted counts, and pooled group rates.

Malformed data, inconsistent populations, conflicting duplicates, and
non-estimable models fail explicitly with a typed `*AnalysisError`.

<p align="center"><a href=http://agouti.org><img src="http://agouti.org/images/agouti_small.png" /></a></p>
