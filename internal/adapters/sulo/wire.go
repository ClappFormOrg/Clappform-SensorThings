// Package sulo integrates SULO's waste-container sensor platform — served
// by the REEN CMS REST API v3 (https://api.reen.com/guide/) — as a
// poll-mode source (ADR-011).
//
// Unlike the Collaborall integration, which needs a standalone reader
// binary because its FROST-Server sits behind a self-signed cert on a
// separate network, REEN is a public HTTPS REST API. So this is a
// PollAdapter driven directly by internal/scheduler: the layer pulls on
// POLL_INTERVAL_SECONDS and hands observations to the same ingest core
// (validator → OMS mapper → FROST writer) that push-mode vendors use.
//
// # Entity mapping
//
// REEN attaches fill-level history to a *container slot*, not to the
// physical container that happens to occupy it ("Fill level history,
// trends, and other data are associated with container slots, not
// containers" — REEN API guide, Key Concepts). A container can be swapped
// out without breaking the measurement series, so the slot is the stable
// sensing platform and therefore our canonical Thing:
//
//	REEN containerSlot  → canonical.Thing        (VendorNativeID = slot id)
//	REEN site lat/lon   → canonical.Thing.Location
//	REEN fillLevels     → canonical.Observation  (FILL_LEVEL, percent)
//	REEN device         → Datastream.SensorMetadata (brand/model/serial)
//
// # Session auth
//
// REEN has no static API key: POST /session exchanges username+password
// for a token passed as the "X-Token" header on every later request
// (REEN API guide, Authentication and Session Security). client.go owns
// that token's lifecycle — see the single-flight re-login there.
package sulo

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

// VendorID is the stable identifier this adapter registers under. It
// appears in Thing.name templates ("Sulo Container 123") and state-store
// rows. Lowercase ASCII per the adapter contract.
//
// It stays "sulo" rather than "reen": SULO is the customer-facing vendor
// the testbed integrates with, and REEN is merely the CMS platform SULO
// runs on. The design doc and every SULO_* env var already use this id.
const VendorID = "sulo"

// APIVersionPath is the version segment of the REEN REST API this adapter
// speaks. Appended to the configured base URL when it is absent, so both
// "https://api.reen.com" and "https://api.reen.com/api/3" work.
const APIVersionPath = "/api/3"

// collectionEnvelope is the common REEN response wrapper. Every response
// carries href/scope/version/generated; set responses add count (the
// number of instances returned) and echo the requested limit.
type collectionEnvelope struct {
	Href      string `json:"href"`
	Scope     string `json:"scope"`
	Generated string `json:"generated"`
	Count     int    `json:"count"`
	Limit     int    `json:"limit"`
}

// sessionResponse is the POST /session body. The token authenticates
// every subsequent request via the X-Token header.
type sessionResponse struct {
	Session struct {
		Token    string `json:"token"`
		Customer int64  `json:"customer"`
		User     int64  `json:"user"`
		Timezone string `json:"timezone"`
	} `json:"session"`
}

// containerSlotsResponse is the GET /containerSlots body.
type containerSlotsResponse struct {
	collectionEnvelope
	ContainerSlots []containerSlotDTO `json:"containerSlots"`
}

// containerSlotDTO is one REEN container slot — our canonical Thing.
//
// Note on Site: the published schema types containerSlot.site as a string
// while every other cross-reference is an integer, so it is decoded
// leniently (see flexInt64).
type containerSlotDTO struct {
	ID                    int64     `json:"id"`
	Name                  string    `json:"name"`
	CustomerKey           string    `json:"customerKey"`
	ContentType           int64     `json:"contentType"`
	SiteContentType       int64     `json:"siteContentType"`
	Site                  flexInt64 `json:"site"`
	FillLevel             *float64  `json:"fillLevel"`
	ObservedFillLevel     *float64  `json:"observedFillLevel"`
	ObservedFillLevelTime string    `json:"observedFillLevelTime"`
	DateWhenFull          string    `json:"dateWhenFull"`
	LastServiceEvent      string    `json:"lastServiceEvent"`
	Container             int64     `json:"container"`
	LastModified          string    `json:"lastModified"`
}

// sitesResponse is the GET /sites body.
type sitesResponse struct {
	collectionEnvelope
	Sites []siteDTO `json:"sites"`
}

// siteDTO is a REEN site — the geographic location a slot lives at.
// Coordinates are (latitude, longitude); canonical.Coord is (lon, lat),
// so mapping.go swaps them.
type siteDTO struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	TypeName    string   `json:"typeName"`
	AreaName    string   `json:"areaName"`
	Address     string   `json:"address"`
	City        string   `json:"city"`
	PostalCode  string   `json:"postalCode"`
	Region      string   `json:"region"`
	Country     string   `json:"country"`
	Latitude    *float64 `json:"latitude"`
	Longitude   *float64 `json:"longitude"`
	CustomerKey string   `json:"customerKey"`
	Timezone    string   `json:"timezone"`
}

// devicesResponse is the GET /devices/linked body.
type devicesResponse struct {
	collectionEnvelope
	Devices []deviceDTO `json:"devices"`
}

// deviceDTO is a REEN sensor device. Only the fields that describe the
// physical sensor (for the FROST Sensor entity) are decoded.
type deviceDTO struct {
	ID             int64  `json:"id"`
	Brand          string `json:"brand"`
	Model          string `json:"model"`
	Serial         string `json:"serial"`
	Container      int64  `json:"container"`
	Installed      string `json:"installed"`
	LastConnection string `json:"lastConnection"`
	Status         struct {
		Signal            *int     `json:"signal"`
		Temperature       *float64 `json:"temperature"`
		BatteryPercentage *int     `json:"batteryPercentage"`
		ConnectionOverdue *bool    `json:"connectionOverdue"`
	} `json:"status"`
}

// contentTypesResponse is the GET /contentTypes body.
type contentTypesResponse struct {
	collectionEnvelope
	ContentTypes []contentTypeDTO `json:"contentTypes"`
}

// contentTypeDTO is a REEN waste fraction (e.g. "Restafval", "Papier").
// Used to describe the Datastream in human terms.
type contentTypeDTO struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	EnglishName  string `json:"englishName"`
	CategoryName string `json:"categoryName"`
	State        string `json:"state"`
}

// label returns the most useful display name for the content type.
func (c contentTypeDTO) label() string {
	if c.Name != "" {
		return c.Name
	}
	if c.EnglishName != "" {
		return c.EnglishName
	}
	return c.CategoryName
}

// fillLevelsResponse is the GET /fillLevels[/containerSlot/{id}] body.
type fillLevelsResponse struct {
	collectionEnvelope
	FillLevels []fillLevelDTO `json:"fillLevels"`
}

// fillLevelDTO is one fill-level estimate — our canonical Observation.
//
// FillLevel is documented as a string percentage ("27"), so it is decoded
// leniently to tolerate a bare JSON number too (see flexFloat64).
//
// Confidence is REEN's analytics quality flag: 100 for normal smoothing,
// 80 when the distance reading was inside minMeasurableDistance, 60 when
// there was no measurement at all, and 0 when the measurement was
// erroneous. See minConfidence in sulo.go for how we gate on it.
type fillLevelDTO struct {
	Time              string      `json:"time"`
	FillLevel         flexFloat64 `json:"fillLevel"`
	Site              int64       `json:"site"`
	SiteName          string      `json:"siteName"`
	SiteContentType   int64       `json:"siteContentType"`
	ContentType       int64       `json:"contentType"`
	ContentTypeName   string      `json:"contentTypeName"`
	ContainerSlot     int64       `json:"containerSlot"`
	ContainerSlotName string      `json:"containerSlotName"`
	Confidence        *int        `json:"confidence"`
	Frozen            *bool       `json:"frozen"`
}

// errorResponse is the JSON body REEN returns on a failed request.
// The field names vary across endpoints, so every plausible spelling is
// decoded and message() picks the first non-empty one.
type errorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
	Detail  string `json:"detail"`
	Reason  string `json:"reason"`
}

// message returns the most descriptive error string available, or "" when
// the body carried none.
func (e errorResponse) message() string {
	for _, s := range []string{e.Error, e.Message, e.Detail, e.Reason} {
		if s != "" {
			return s
		}
	}
	return ""
}

// flexInt64 decodes a JSON integer that the REEN schema sometimes declares
// as a string (containerSlot.site). Absent, null, and "" decode to 0.
type flexInt64 int64

func (f *flexInt64) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		*f = 0
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		if s == "" {
			*f = 0
			return nil
		}
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return fmt.Errorf("sulo: %q is not an integer: %w", s, err)
		}
		*f = flexInt64(n)
		return nil
	}
	var n int64
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*f = flexInt64(n)
	return nil
}

// flexFloat64 decodes a JSON number that REEN documents as a string
// (fillLevels[].fillLevel). It records whether a usable value was present
// so callers can distinguish "0 percent" from "field omitted".
type flexFloat64 struct {
	Value float64
	Set   bool
}

func (f *flexFloat64) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		*f = flexFloat64{}
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		if s == "" {
			*f = flexFloat64{}
			return nil
		}
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return fmt.Errorf("sulo: %q is not a number: %w", s, err)
		}
		*f = flexFloat64{Value: v, Set: true}
		return nil
	}
	var v float64
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*f = flexFloat64{Value: v, Set: true}
	return nil
}
