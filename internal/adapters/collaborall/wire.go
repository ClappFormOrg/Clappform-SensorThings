// Package collaborall integrates the Collaborall FROST-Server as a source.
//
// It has two halves that share this package so the wire format cannot
// drift:
//
//   - PushAdapter (push.go) runs INSIDE the translation-layer service. It
//     authenticates and decodes batches posted to POST /ingest/collaborall.
//   - The reader binary (cmd/collaborall-reader) runs SEPARATELY. It reads
//     Sensors/Observations from Collaborall's FROST DB, builds an Envelope,
//     and POSTs it to the push endpoint.
//
// Keeping the FROST-read logic in the reader keeps the service decoupled
// from Collaborall's quirks (self-signed cert, custom entities); the
// service only ever sees the canonical types in an Envelope.
package collaborall

import (
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/adapters"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/canonical"
)

// VendorID is the stable identifier this integration registers under and
// the {vendorID} path segment on POST /ingest/collaborall. Lowercase
// ASCII per the adapter contract.
const VendorID = "collaborall"

// Envelope is the JSON body the reader POSTs to the push endpoint. It is
// adapters.DecodedBatch with an explicit, tagged wire shape so the
// contract is documented and both ends agree by construction.
type Envelope struct {
	Things       []ThingWithStreams      `json:"things"`
	Observations []canonical.Observation `json:"observations"`
}

// ThingWithStreams pairs a Thing with the Datastreams declared for it,
// mirroring adapters.DecodedThing.
type ThingWithStreams struct {
	Thing       canonical.Thing        `json:"thing"`
	Datastreams []canonical.Datastream `json:"datastreams"`
}

// ToBatch converts the wire Envelope into the canonical DecodedBatch the
// ingest core consumes.
func (e Envelope) ToBatch() adapters.DecodedBatch {
	things := make([]adapters.DecodedThing, 0, len(e.Things))
	for _, t := range e.Things {
		things = append(things, adapters.DecodedThing{
			Thing:       t.Thing,
			Datastreams: t.Datastreams,
		})
	}
	return adapters.DecodedBatch{
		Things:       things,
		Observations: e.Observations,
	}
}

// FromBatch builds a wire Envelope from a DecodedBatch. Used by the reader
// binary to serialise what it read from Collaborall.
func FromBatch(b adapters.DecodedBatch) Envelope {
	things := make([]ThingWithStreams, 0, len(b.Things))
	for _, t := range b.Things {
		things = append(things, ThingWithStreams{
			Thing:       t.Thing,
			Datastreams: t.Datastreams,
		})
	}
	return Envelope{
		Things:       things,
		Observations: b.Observations,
	}
}
