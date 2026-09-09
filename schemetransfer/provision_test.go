package schemetransfer

import (
	"strings"
	"testing"
)

func TestProvisionRequestOwnershipAndValues(t *testing.T) {
	base := ProvisionRequest{Kind: "projectVariable", Action: "create", Key: "price", Name: "Price", ValueType: "number", Value: []byte(`9007199254740993`)}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*ProvisionRequest){
		func(r *ProvisionRequest) { r.Kind = "integrationVariable" },
		func(r *ProvisionRequest) { r.Kind = "subjectVariable" },
		func(r *ProvisionRequest) { r.Action = "reuse" },
		func(r *ProvisionRequest) { r.Value = nil },
		func(r *ProvisionRequest) { r.Value = []byte(`"12"`) },
		func(r *ProvisionRequest) { r.Key = "project.price" },
		func(r *ProvisionRequest) { r.Value = []byte(`{"a":1,"a":2}`) },
		func(r *ProvisionRequest) { r.Description = string([]byte{0xff}) },
		func(r *ProvisionRequest) { r.Name = strings.Repeat("я", 257) },
	} {
		input := base
		change(&input)
		if input.Validate() == nil {
			t.Fatalf("invalid input accepted: kind=%s action=%s", input.Kind, input.Action)
		}
	}
	base.Value = []byte(`null`)
	if base.Validate() != nil {
		t.Fatal("explicit null rejected")
	}
	subject := ProvisionRequest{Kind: "subjectVariable", Action: "create", Key: "name", Name: "Name", ValueType: "string"}
	if subject.Validate() != nil {
		t.Fatal("empty subject definition rejected")
	}
	tag := ProvisionRequest{Kind: "tag", Action: "create", Key: "Paid-Key", Name: "Paid"}
	if tag.Validate() != nil {
		t.Fatal("case-sensitive tag key rejected")
	}
}
func TestProvisionDigestAndReceipt(t *testing.T) {
	a := ProvisionRequest{Kind: "projectVariable", Action: "create", Key: "data", Name: "Data", ValueType: "object", Value: []byte(`{"b":2,"a":9007199254740993}`)}
	b := a
	b.Value = []byte(`{ "a":9007199254740993, "b":2 }`)
	da, err := a.Digest()
	if err != nil {
		t.Fatal(err)
	}
	db, err := b.Digest()
	if err != nil || da != db {
		t.Fatal("object order changed digest")
	}
	b.Value = []byte(`{"a":9007199254740992,"b":2}`)
	db, _ = b.Digest()
	if da == db {
		t.Fatal("large integers rounded")
	}
	request := ProvisionRequest{Kind: "subjectVariable", Action: "reuse", ID: "10000000-0000-4000-8000-000000000001", Key: "name", ValueType: "string"}
	result := ProvisionResult{Kind: request.Kind, ID: request.ID, Key: request.Key, ValueType: request.ValueType}
	if request.Validate() != nil || result.Validate(request) != nil {
		t.Fatal("valid reuse rejected")
	}
	result.Created = true
	if result.Validate(request) == nil {
		t.Fatal("reuse unexpectedly created a definition")
	}
	result.Created = false
	result.Key = "other"
	if result.Validate(request) == nil {
		t.Fatal("changed target key accepted")
	}
}
