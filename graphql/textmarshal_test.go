package graphql_test

import (
	"encoding/hex"
	"errors"
	"fmt"
	"testing"

	"github.com/hiett/lightning/graphql/schemabuilder"
	"github.com/hiett/lightning/internal/testgraphql"
)

func TestTextMarshaling(t *testing.T) {
	schema := schemabuilder.NewSchema()

	type Inner struct {
		PtrUuid      *Uuid
		Uuid         Uuid
		UuidSlice    []Uuid
		PtrUuidSlice []*Uuid
	}

	o := schema.Object("Inner", Inner{})
	o.FieldFunc("uuidFunc", func(inner Inner) Uuid {
		return inner.Uuid
	})
	o.FieldFunc("uuidSliceFunc", func(inner Inner) []Uuid {
		return inner.UuidSlice
	})
	o.FieldFunc("ptrUuidSliceFunc", func(inner Inner) []*Uuid {
		return inner.PtrUuidSlice
	})
	o.FieldFunc("invalidUuidFunc", func(inner Inner) Uuid {
		return Uuid{marshalError: errors.New("invalidUUID")} // Invalid Uuid type (for testing)
	})

	PtrUuidSliceResp := []*Uuid{
		NewUuidPtr(),
		nil,
		NewUuidPtr(),
	}

	query := schema.Query()
	query.FieldFunc("inner", func(input struct {
		InputPtrUuid   *Uuid
		InputUuid      Uuid
		InputUuidSlice []Uuid
	}) Inner {
		return Inner{
			PtrUuid:      input.InputPtrUuid,
			Uuid:         input.InputUuid,
			UuidSlice:    input.InputUuidSlice,
			PtrUuidSlice: PtrUuidSliceResp,
		}
	})

	_ = schema.Mutation()

	builtSchema := schema.MustBuild()

	snap := testgraphql.NewSnapshotter(t, builtSchema)
	defer snap.Verify()

	snap.SnapshotQuery("happy path all inputs and outputs", `{
		inner(
			inputPtrUuid: "74771078-5edb-4733-88f2-000000000000", 
			inputUuid: "74771078-5edb-4733-88f2-111111111111", 
			inputUuidSlice: ["74771078-5edb-4733-88f2-222222222222", "74771078-5edb-4733-88f2-333333333333", "74771078-5edb-4733-88f2-444444444444"], 
		) { 
			ptrUuid
			uuid
			uuidSlice
			ptrUuidSlice
			uuidFunc
			uuidSliceFunc
			ptrUuidSliceFunc
		}
	}`)

	snap.SnapshotQuery("invalid ptr uuid input", `{
		inner(
			inputPtrUuid: "invaliduuid", 
			inputUuid: "74771078-5edb-4733-88f2-111111111111", 
			inputUuidSlice: ["74771078-5edb-4733-88f2-222222222222"], 
		) { 
			ptrUuid
		}
	}`, testgraphql.RecordError)

	snap.SnapshotQuery("invalid uuid input", `{
		inner(
			inputPtrUuid: "74771078-5edb-4733-88f2-000000000000", 
			inputUuid: "invaliduuid", 
			inputUuidSlice: ["74771078-5edb-4733-88f2-222222222222"], 
		) { 
			uuid
		}
	}`, testgraphql.RecordError)

	snap.SnapshotQuery("invalid uuid slice input", `{
		inner(
			inputPtrUuid: "74771078-5edb-4733-88f2-000000000000", 
			inputUuid: "74771078-5edb-4733-88f2-111111111111", 
			inputUuidSlice: ["74771078-5edb-4733-88f2-222222222222", "invaliduuid"], 
		) { 
			uuidSlice
		}
	}`, testgraphql.RecordError)

	snap.SnapshotQuery("invalid uuid func response", `{
		inner(
			inputPtrUuid: "74771078-5edb-4733-88f2-000000000000", 
			inputUuid: "74771078-5edb-4733-88f2-111111111111", 
			inputUuidSlice: ["74771078-5edb-4733-88f2-222222222222"], 
		) { 
			invalidUuidFunc
		}
	}`, testgraphql.RecordError)

}

// Uuid is a testable version of a "Text Marshalable" type.
type Uuid struct {
	bytes        [16]byte
	marshalError error
}

func NewUuidPtr() *Uuid {
	u := NewUuid()
	return &u
}

var counter = byte(0)

func NewUuid() Uuid {
	u := Uuid{
		bytes: [16]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, byte(counter)},
	}
	counter += 1
	return u
}

func (u Uuid) MarshalText() ([]byte, error) {
	if u.marshalError != nil {
		return nil, u.marshalError
	}
	return []byte(u.string()), nil
}

func (u Uuid) string() string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", u.bytes[0:4], u.bytes[4:6], u.bytes[6:8], u.bytes[8:10], u.bytes[10:16])
}

func (u *Uuid) UnmarshalText(data []byte) error {
	if string(data) == "" {
		return nil
	}

	uu, err := parseUuid(string(data))
	if err != nil {
		return err
	}

	*u = Uuid{bytes: uu}
	return nil
}

// parseUuid parses the canonical 8-4-4-4-12 hexadecimal UUID form.
func parseUuid(s string) ([16]byte, error) {
	var out [16]byte
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return out, fmt.Errorf("uuid: invalid UUID string: %s", s)
	}
	stripped := s[0:8] + s[9:13] + s[14:18] + s[19:23] + s[24:36]
	b, err := hex.DecodeString(stripped)
	if err != nil {
		return out, fmt.Errorf("uuid: invalid UUID string: %s", s)
	}
	copy(out[:], b)
	return out, nil
}
