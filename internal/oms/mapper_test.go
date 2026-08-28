package oms

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ClappFormOrg/Clappform-SensorThings/internal/canonical"
)

func passthroughDatastream() canonical.Datastream {
	return canonical.Datastream{
		ThingVendorNativeID:        "10",
		ObservedProperty:           "Pomp-1 druk", // stream key = source ds name
		Name:                       "Pomp-1 druk",
		Description:                "pump pressure",
		ObservedPropertyName:       "Druk",
		ObservedPropertyDefinition: "http://qudt.org/vocab/quantitykind/Pressure",
		UnitName:                   "Kilopascal",
		UnitSymbol:                 "kPa",
		UnitDefinition:             "http://qudt.org/vocab/unit/KiloPA",
		ObservationType:            "http://www.opengis.net/def/observationType/OGC-OM/2.0/OM_Measurement",
		SensorName:                 "Drukopnemer pomp-1",
		SensorMetadata:             map[string]string{"model": "M1"},
	}
}

func TestPassthroughNamesUsePrefixAndSourceValues(t *testing.T) {
	cfg := DefaultConfig()
	cfg.EntityNamePrefix = "CF_"
	m := New(cfg)

	thing := canonical.Thing{VendorID: "collaborall", VendorNativeID: "10", Name: "Thing A"}
	d := passthroughDatastream()

	if got := m.ThingEntityName(thing); got != "CF_Thing A" {
		t.Errorf("ThingEntityName = %q", got)
	}
	if got := m.DatastreamEntityName(thing, d); got != "CF_Pomp-1 druk" {
		t.Errorf("DatastreamEntityName = %q", got)
	}
	if got := m.ObservedPropertyEntityName(d); got != "CF_Druk" {
		t.Errorf("ObservedPropertyEntityName = %q", got)
	}
	if got := m.SensorEntityName(thing, d); got != "CF_Drukopnemer pomp-1" {
		t.Errorf("SensorEntityName = %q", got)
	}
	if got := m.FoIEntityName(thing); !strings.HasPrefix(got, "CF_") {
		t.Errorf("FoIEntityName not prefixed: %q", got)
	}
}

func TestPassthroughPayloadsPreserveSourceDetail(t *testing.T) {
	cfg := DefaultConfig()
	cfg.EntityNamePrefix = "CF_"
	m := New(cfg)
	thing := canonical.Thing{VendorID: "collaborall", VendorNativeID: "10", Name: "Thing A"}
	d := passthroughDatastream()

	op := m.ObservedPropertyPayload(d)
	if op.Name != "CF_Druk" || op.Definition != d.ObservedPropertyDefinition {
		t.Fatalf("ObservedPropertyPayload = %+v", op)
	}

	ds := m.DatastreamPayload(thing, d, 1, 2, 3)
	if ds.Name != "CF_Pomp-1 druk" || ds.Description != "pump pressure" {
		t.Fatalf("Datastream name/desc = %+v", ds)
	}
	if ds.UnitOfMeasurement.Symbol != "kPa" || ds.UnitOfMeasurement.Name != "Kilopascal" {
		t.Fatalf("unit not passed through: %+v", ds.UnitOfMeasurement)
	}
	if !strings.Contains(ds.ObservationType, "OM_Measurement") {
		t.Fatalf("observationType not passed through: %q", ds.ObservationType)
	}
}

func TestObservationPayloadNonNumericResult(t *testing.T) {
	m := New(DefaultConfig())
	o := canonical.Observation{
		PhenomenonTime: time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC),
		ResultTime:     time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC),
		ResultRaw:      json.RawMessage("true"),
	}
	body := m.ObservationPayload(o, 0)
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"result":true`) {
		t.Fatalf("verbatim boolean result not emitted: %s", b)
	}
}

func TestObservationPayloadNumericResult(t *testing.T) {
	m := New(DefaultConfig())
	o := canonical.Observation{
		PhenomenonTime: time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC),
		ResultTime:     time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC),
		Result:         73.5,
	}
	body := m.ObservationPayload(o, 0)
	b, _ := json.Marshal(body)
	if !strings.Contains(string(b), `"result":73.5`) {
		t.Fatalf("numeric result not emitted: %s", b)
	}
}

func TestFillLevelFallbackUnaffectedByEmptyPrefix(t *testing.T) {
	m := New(DefaultConfig()) // no prefix, no passthrough fields
	thing := canonical.Thing{VendorID: "sulo", VendorNativeID: "C1"}
	d := canonical.Datastream{ThingVendorNativeID: "C1", ObservedProperty: canonical.FillLevel, Unit: canonical.Percent}

	if got := m.ThingEntityName(thing); got != "Sulo Container C1" {
		t.Errorf("fill-level Thing name changed: %q", got)
	}
	op := m.ObservedPropertyPayload(d)
	if op.Name != "Fill level" {
		t.Errorf("fill-level OP name changed: %q", op.Name)
	}
	ds := m.DatastreamPayload(thing, d, 1, 2, 3)
	if ds.UnitOfMeasurement.Symbol != "%" {
		t.Errorf("fill-level unit changed: %+v", ds.UnitOfMeasurement)
	}
}
