package agouti

import (
	"fmt"
	"strings"
	"time"
)

// A SnapshotPageConfig locates the four device snapshot tables on the
// currently open page. Empty fields fall back to the default selectors
// "#devices", "#online-intervals", "#maintenance-intervals", and
// "#events".
//
// The page is expected to expose one HTML table per dataset. Every table
// element must carry a data-version attribute naming the snapshot version
// and a data-complete attribute set to "true" once the version is final;
// all four tables must belong to the same completed version. Data rows
// are <tr> elements whose cells carry data-field attributes:
//
//	devices:               device-id, plus one cell per device covariate
//	online-intervals:      device-id, start, end
//	maintenance-intervals: device-id, start, end
//	events:                id, device-id, time, and optionally kind
//
// Header rows (cells without data-field) are ignored. Timestamps may be
// RFC 3339 with any offset (they are unified to UTC) or naive UTC values
// such as "2006-01-02 15:04:05".
type SnapshotPageConfig struct {
	DevicesTable     string
	OnlineTable      string
	MaintenanceTable string
	EventsTable      string
}

func (c SnapshotPageConfig) defaults() SnapshotPageConfig {
	if c.DevicesTable == "" {
		c.DevicesTable = "#devices"
	}
	if c.OnlineTable == "" {
		c.OnlineTable = "#online-intervals"
	}
	if c.MaintenanceTable == "" {
		c.MaintenanceTable = "#maintenance-intervals"
	}
	if c.EventsTable == "" {
		c.EventsTable = "#events"
	}
	return c
}

// snapshotExtractorScript reads the four snapshot tables in a single
// round trip. It is ES5-only so that every WebDriver Agouti supports can
// run it. The body is wrapped in a function by Page.RunScript and
// receives the table selectors as its "selectors" argument.
const snapshotExtractorScript = `
	var tables = {};
	var failed = null;
	var names = [];
	for (var key in selectors) { names.push(key); }
	names.sort();
	for (var i = 0; i < names.length; i++) {
		var name = names[i];
		var root = document.querySelector(selectors[name]);
		if (!root) { failed = "no element found for selector: " + selectors[name]; break; }
		var data = {version: "", complete: "", rows: []};
		if (root.getAttribute) {
			data.version = root.getAttribute("data-version") || "";
			data.complete = root.getAttribute("data-complete") || "";
		}
		var rows = root.querySelectorAll("tr");
		for (var r = 0; r < rows.length; r++) {
			var cells = rows[r].querySelectorAll("[data-field]");
			if (!cells.length) { continue; }
			var row = {};
			for (var c = 0; c < cells.length; c++) {
				row[cells[c].getAttribute("data-field")] =
					cells[c].textContent.replace(/^\s+|\s+$/g, "");
			}
			data.rows.push(row);
		}
		tables[name] = data;
	}
	if (failed) { return {error: failed}; }
	return {tables: tables};
`

// ReadDeviceSnapshot reads the device attributes, online intervals,
// maintenance intervals, and stable-ID events of one completed snapshot
// version from the currently open page. The page must already be logged
// in; the method only reads the open page and does not navigate.
//
// All four tables must declare the same data-version and be marked
// data-complete="true", otherwise a VersionError is returned. Malformed
// fields (missing device IDs, unparseable timestamps) fail with a
// TypeError. The returned snapshot is a complete input for
// AnalyzeDeviceRates.
func (p *Page) ReadDeviceSnapshot(config SnapshotPageConfig) (*DeviceSnapshot, error) {
	config = config.defaults()
	selectors := map[string]string{
		"devices":     config.DevicesTable,
		"online":      config.OnlineTable,
		"maintenance": config.MaintenanceTable,
		"events":      config.EventsTable,
	}
	extracted := struct {
		Error  string                      `json:"error"`
		Tables map[string]rawSnapshotTable `json:"tables"`
	}{}
	if err := p.RunScript(snapshotExtractorScript, map[string]interface{}{"selectors": selectors}, &extracted); err != nil {
		return nil, fmt.Errorf("failed to read device snapshot from page: %s", err)
	}
	if extracted.Error != "" {
		return nil, fmt.Errorf("failed to read device snapshot from page: %s", extracted.Error)
	}
	return buildSnapshot(extracted.Tables)
}

// AnalyzeDeviceRates reads the device snapshot from the currently open
// page and analyzes it in one call. See ReadDeviceSnapshot and the
// package-level AnalyzeDeviceRates.
func (p *Page) AnalyzeDeviceRates(config SnapshotPageConfig, spec AnalysisSpec) (*RateAnalysis, error) {
	snapshot, err := p.ReadDeviceSnapshot(config)
	if err != nil {
		return nil, err
	}
	return AnalyzeDeviceRates(snapshot, spec)
}

type rawSnapshotTable struct {
	Version  string              `json:"version"`
	Complete string              `json:"complete"`
	Rows     []map[string]string `json:"rows"`
}

var snapshotTableNames = []string{"devices", "online", "maintenance", "events"}

func buildSnapshot(tables map[string]rawSnapshotTable) (*DeviceSnapshot, error) {
	for _, name := range snapshotTableNames {
		if _, ok := tables[name]; !ok {
			return nil, fmt.Errorf("extracted snapshot is missing the %q table", name)
		}
	}
	version := ""
	versionTable := ""
	for _, name := range snapshotTableNames {
		table := tables[name]
		if table.Version == "" {
			return nil, newAnalysisError(VersionError, "table %q does not declare a snapshot version (missing data-version)", name)
		}
		if version == "" {
			version, versionTable = table.Version, name
		} else if table.Version != version {
			return nil, newAnalysisError(VersionError, "tables %q and %q belong to different snapshot versions (%q and %q)", versionTable, name, version, table.Version)
		}
		if !strings.EqualFold(table.Complete, "true") {
			return nil, newAnalysisError(VersionError, "snapshot version %q of table %q is not marked complete", table.Version, name)
		}
	}

	var devices []Device
	for i, row := range tables["devices"].Rows {
		id := row["device-id"]
		if id == "" {
			return nil, newAnalysisError(TypeError, "devices row %d is missing a device-id", i+1)
		}
		covariates := map[string]string{}
		for field, value := range row {
			if field != "device-id" {
				covariates[field] = value
			}
		}
		devices = append(devices, Device{ID: id, Covariates: covariates})
	}

	online, err := buildIntervals(tables["online"].Rows, "online")
	if err != nil {
		return nil, err
	}
	maintenance, err := buildIntervals(tables["maintenance"].Rows, "maintenance")
	if err != nil {
		return nil, err
	}

	var events []DeviceEvent
	for i, row := range tables["events"].Rows {
		id := row["id"]
		if id == "" {
			return nil, newAnalysisError(TypeError, "events row %d is missing a stable event id", i+1)
		}
		deviceID := row["device-id"]
		if deviceID == "" {
			return nil, newAnalysisError(TypeError, "events row %d is missing a device-id", i+1)
		}
		at, err := parseSnapshotTime(row["time"])
		if err != nil {
			return nil, newAnalysisError(TypeError, "events row %d has invalid time: %s", i+1, err)
		}
		events = append(events, DeviceEvent{ID: id, DeviceID: deviceID, Time: at, Kind: row["kind"]})
	}

	return &DeviceSnapshot{
		Version:     version,
		Devices:     devices,
		Online:      online,
		Maintenance: maintenance,
		Events:      events,
	}, nil
}

func buildIntervals(rows []map[string]string, table string) ([]Interval, error) {
	var intervals []Interval
	for i, row := range rows {
		id := row["device-id"]
		if id == "" {
			return nil, newAnalysisError(TypeError, "%s row %d is missing a device-id", table, i+1)
		}
		start, err := parseSnapshotTime(row["start"])
		if err != nil {
			return nil, newAnalysisError(TypeError, "%s row %d has invalid start: %s", table, i+1, err)
		}
		end, err := parseSnapshotTime(row["end"])
		if err != nil {
			return nil, newAnalysisError(TypeError, "%s row %d has invalid end: %s", table, i+1, err)
		}
		intervals = append(intervals, Interval{DeviceID: id, Start: start, End: end})
	}
	return intervals, nil
}

// snapshotTimeFormats are tried in order. RFC 3339 values carry their own
// offset; the naive formats are read as UTC. Every parsed time is
// normalized to UTC so that all interval arithmetic happens at unified
// UTC instants.
var snapshotTimeFormats = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

func parseSnapshotTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, fmt.Errorf("empty timestamp")
	}
	for _, format := range snapshotTimeFormats {
		if parsed, err := time.Parse(format, value); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unparseable timestamp %q", value)
}
