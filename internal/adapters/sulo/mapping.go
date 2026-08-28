package sulo

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ClappFormOrg/Clappform-SensorThings/internal/canonical"
)

// reenTimeLayout is the timestamp format REEN documents for the "after" and
// "until" query parameters: ISO 8601, UTC, second precision.
const reenTimeLayout = "2006-01-02T15:04:05Z"

// slotView is everything the adapter knows about one container slot after a
// discovery pass: the slot itself plus the related entities that give it a
// location, a sensor and a human-readable waste fraction. The related
// pointers are nil when REEN did not return them (or when the account lacks
// rights to read them), so every consumer must nil-check.
type slotView struct {
	slot        containerSlotDTO
	site        *siteDTO
	device      *deviceDTO
	contentType *contentTypeDTO
}

// nativeID is the canonical VendorNativeID for the slot.
func (v slotView) nativeID() string {
	return strconv.FormatInt(v.slot.ID, 10)
}

// coord returns the slot's location, taken from its site. REEN reports
// (latitude, longitude); canonical.Coord is GeoJSON order (lon, lat), so
// they are swapped here — inside the adapter, per the canonical.Coord
// contract. ok=false when the site is unknown or has no coordinates.
func (v slotView) coord() (canonical.Coord, bool) {
	if v.site == nil || v.site.Latitude == nil || v.site.Longitude == nil {
		return canonical.Coord{}, false
	}
	return canonical.Coord{Lon: *v.site.Longitude, Lat: *v.site.Latitude}, true
}

// contentLabel is the display name of the waste fraction this slot holds,
// or "" when unknown.
func (v slotView) contentLabel() string {
	if v.contentType == nil {
		return ""
	}
	return v.contentType.label()
}

// toCanonicalThing projects a slot into a canonical Thing.
//
// Name is deliberately left empty. The OMS mapper then synthesises the
// Implementation-Contract name ("Sulo Container <slot id>"), which is the
// same string the scheduler writes to the state store via oms.ThingName —
// so the state store and FROST always agree on a Thing's identity. The
// REEN-side slot and site names are preserved in Description and
// Properties instead of overriding that.
func (v slotView) toCanonicalThing() canonical.Thing {
	t := canonical.Thing{
		VendorID:       VendorID,
		VendorNativeID: v.nativeID(),
		Description:    v.describe(),
		Properties:     v.properties(),
	}
	if c, ok := v.coord(); ok {
		t.Location = c
	}
	return t
}

// describe builds a human-readable Thing description from whatever related
// entities resolved.
func (v slotView) describe() string {
	var b strings.Builder
	b.WriteString("SULO waste container slot")
	if v.slot.Name != "" {
		fmt.Fprintf(&b, " %q", v.slot.Name)
	}
	if label := v.contentLabel(); label != "" {
		fmt.Fprintf(&b, " for %s", label)
	}
	if v.site != nil && v.site.Name != "" {
		fmt.Fprintf(&b, " at site %q", v.site.Name)
	}
	if v.site != nil {
		if loc := strings.TrimSpace(strings.Join(nonEmpty(v.site.Address, v.site.PostalCode, v.site.City), ", ")); loc != "" {
			fmt.Fprintf(&b, " (%s)", loc)
		}
	}
	return b.String()
}

// properties carries the REEN identifiers and site metadata onto the FROST
// Thing so an STA consumer can trace an entity back to the source system.
// Empty values are omitted rather than written as "".
func (v slotView) properties() map[string]string {
	p := map[string]string{
		"reen_container_slot_id": strconv.FormatInt(v.slot.ID, 10),
	}
	setIf(p, "reen_container_slot_name", v.slot.Name)
	setIf(p, "reen_customer_key", v.slot.CustomerKey)
	setIfNonZero(p, "reen_content_type_id", v.slot.ContentType)
	setIf(p, "reen_content_type_name", v.contentLabel())
	setIfNonZero(p, "reen_site_content_type_id", v.slot.SiteContentType)
	setIfNonZero(p, "reen_site_id", int64(v.slot.Site))
	setIfNonZero(p, "reen_container_id", v.slot.Container)

	if v.site != nil {
		setIf(p, "reen_site_name", v.site.Name)
		setIf(p, "reen_site_type", v.site.TypeName)
		setIf(p, "reen_site_area", v.site.AreaName)
		setIf(p, "address", v.site.Address)
		setIf(p, "postal_code", v.site.PostalCode)
		setIf(p, "city", v.site.City)
		setIf(p, "region", v.site.Region)
		setIf(p, "country", v.site.Country)
	}
	if v.device != nil {
		setIfNonZero(p, "reen_device_id", v.device.ID)
		setIf(p, "device_serial", v.device.Serial)
	}
	return p
}

// toCanonicalDatastream builds the single fill-level stream for a slot.
//
// The Passthrough* naming fields (Name / ObservedPropertyName / Unit*) are
// left unset on purpose: this is a fixed-phenomenon poll vendor, so the OMS
// mapper's canonical fill-level names and the UCUM percent unit apply.
// Only Description is supplied, because the mapper prefers a caller-given
// description and the content type makes it far more informative than the
// generic fallback.
func (v slotView) toCanonicalDatastream(cadenceSeconds int) canonical.Datastream {
	d := canonical.Datastream{
		ThingVendorNativeID:    v.nativeID(),
		ObservedProperty:       canonical.FillLevel,
		Unit:                   canonical.Percent,
		SensorMetadata:         v.sensorMetadata(),
		ExpectedCadenceSeconds: cadenceSeconds,
		Description:            v.datastreamDescription(),
	}
	return d
}

func (v slotView) datastreamDescription() string {
	label := v.contentLabel()
	if label == "" {
		label = "waste"
	}
	name := v.slot.Name
	if name == "" {
		name = v.nativeID()
	}
	return fmt.Sprintf("Fill level (%% of capacity) of %s in SULO container slot %s", label, name)
}

// sensorMetadata describes the physical device for the FROST Sensor
// entity. The "model" and "firmware_version" keys are read by the OMS
// mapper to build the Sensor name, so they are always present — falling
// back to a platform-level label when no device is linked, which keeps
// slots without a resolvable device from all collapsing onto the same
// "unknown unknown" Sensor entity as other vendors.
func (v slotView) sensorMetadata() map[string]string {
	m := map[string]string{
		"source_system": "reen",
	}
	if v.device == nil {
		m["model"] = "reen-unlinked"
		m["firmware_version"] = "unknown"
		return m
	}

	model := v.device.Model
	if model == "" {
		model = "reen-device"
	}
	m["model"] = model
	// REEN exposes no firmware version on a device, so the Sensor identity
	// is (brand, model) only; "unknown" keeps the mapper's name template
	// stable rather than leaving a dangling segment.
	m["firmware_version"] = "unknown"
	setIf(m, "brand", v.device.Brand)
	setIf(m, "serial", v.device.Serial)
	setIf(m, "reen_device_id", strconv.FormatInt(v.device.ID, 10))
	setIf(m, "installed", v.device.Installed)
	setIf(m, "last_connection", v.device.LastConnection)
	if s := v.device.Status.Signal; s != nil {
		m["signal_percent"] = strconv.Itoa(*s)
	}
	if b := v.device.Status.BatteryPercentage; b != nil {
		m["battery_percent"] = strconv.Itoa(*b)
	}
	return m
}

// parseREENTime parses a REEN ISO-8601 timestamp. REEN documents UTC with
// second precision but RFC3339 parsing also accepts offsets and fractional
// seconds, so both are tolerated. The result is always UTC.
func parseREENTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// observationID builds a stable, unique identifier for one fill-level
// reading: the slot it belongs to plus its timestamp. The pair is unique
// in REEN (one estimate per slot per instant) and stable across re-polls,
// which is what the state store's write log needs for idempotency.
func observationID(slotID int64, t time.Time) string {
	return fmt.Sprintf("%d@%s", slotID, t.UTC().Format(time.RFC3339))
}

func setIf(m map[string]string, key, value string) {
	if v := strings.TrimSpace(value); v != "" {
		m[key] = v
	}
}

func setIfNonZero(m map[string]string, key string, value int64) {
	if value != 0 {
		m[key] = strconv.FormatInt(value, 10)
	}
}

func nonEmpty(in ...string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if v := strings.TrimSpace(s); v != "" {
			out = append(out, v)
		}
	}
	return out
}
