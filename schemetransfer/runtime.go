package schemetransfer

import (
	"encoding/json"
	"strconv"
	"strings"
)

type CopyOperation string

const (
	ResourceValues       CopyOperation = "resourceValues"
	PrepareCopy          CopyOperation = "prepareCopy"
	ValidateCopySettings CopyOperation = "validateCopySettings"
)

// ValidateCallbackRequest validates the original bytes, before decoding can
// erase unknown fields, duplicate keys or explicitly supplied empty fields.
func ValidateCallbackRequest(operation CopyOperation, raw []byte) error {
	var definition string
	switch operation {
	case ResourceValues:
		definition = "lookupRequest"
	case PrepareCopy:
		definition = "prepareCopyRequest"
	case ValidateCopySettings:
		definition = "validateCopySettingsRequest"
	default:
		return invalid("unknownCopyOperation", "")
	}
	return Validate(definition, raw)
}

// ValidateCallbackResponse is shared by the integration receiver and platform
// transport. The response is always checked against the original request.
func ValidateCallbackResponse(operation CopyOperation, request, response []byte) error {
	if err := ValidateCallbackRequest(operation, request); err != nil {
		return err
	}
	switch operation {
	case ResourceValues:
		var req LookupRequest
		_ = json.Unmarshal(request, &req)
		if req.Mode == "identify" {
			if err := Validate("identifyResponse", response); err != nil {
				return err
			}
			var result IdentifyResponse
			_ = json.Unmarshal(response, &result)
			return result.ValidateFor(req)
		}
		if err := Validate("lookupResponse", response); err != nil {
			return err
		}
		var result LookupResponse
		_ = json.Unmarshal(response, &result)
		return result.ValidateFor(req)
	case PrepareCopy:
		if err := Validate("prepareCopyResponse", response); err != nil {
			return err
		}
		var req PrepareCopyRequest
		var result PrepareCopyResponse
		_ = json.Unmarshal(request, &req)
		_ = json.Unmarshal(response, &result)
		return result.ValidateFor(req)
	case ValidateCopySettings:
		if err := Validate("validationResult", response); err != nil {
			return err
		}
		var result ValidationResult
		_ = json.Unmarshal(response, &result)
		return result.Validate()
	default:
		return invalid("unknownCopyOperation", "")
	}
}

// Validate checks the tagged request before a callback is invoked. An empty
// resolve/identify batch is invalid and can never turn into a search.
func (r LookupRequest) Validate() error {
	if (r.Mode == "search" && (r.IDs != nil || r.Values != nil)) ||
		(r.Mode == "resolve" && r.Values != nil) || (r.Mode == "identify" && r.IDs != nil) {
		return invalid("invalidLookupMode", "/mode")
	}
	return validateTyped("lookupRequest", r)
}

// ValidateFor also checks relationships that JSON Schema cannot express:
// pagination, unique identities and membership of a resolve response in its
// original batch. Missing identities are omitted, never invented or substituted.
func (r LookupResponse) ValidateFor(request LookupRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if request.Mode == "identify" {
		return invalid("invalidLookupMode", "/mode")
	}
	if err := validateTyped("lookupResponse", r); err != nil {
		return err
	}
	limit := 100
	if request.Limit > 0 {
		limit = request.Limit
	}
	allowed := map[string]bool{}
	if request.Mode == "resolve" {
		limit = len(request.IDs)
		for _, id := range request.IDs {
			allowed[id] = true
		}
		if r.NextCursor != "" {
			return invalid("unexpectedCursor", "/nextCursor")
		}
	}
	if len(r.Items) > limit {
		return invalid("tooManyItems", "/items")
	}
	seen := map[string]bool{}
	for i, item := range r.Items {
		path := Pointer("/items", strconv.Itoa(i))
		if seen[item.ID] {
			return invalid("duplicateResourceIdentity", Pointer(path, "id"))
		}
		seen[item.ID] = true
		if request.Mode == "resolve" && !allowed[item.ID] {
			return invalid("unexpectedResourceIdentity", Pointer(path, "id"))
		}
		if err := validateResourceItem(item, path); err != nil {
			return err
		}
	}
	return nil
}

// ValidateFor requires exactly one result per input, including missing and
// ambiguous values. Several inputs may legitimately identify the same resource.
func (r IdentifyResponse) ValidateFor(request LookupRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if request.Mode != "identify" {
		return invalid("invalidLookupMode", "/mode")
	}
	if err := validateTyped("identifyResponse", r); err != nil {
		return err
	}
	if len(r.Matches) != len(request.Values) {
		return invalid("incompleteIdentifyResponse", "/matches")
	}
	seen := map[int]bool{}
	for i, match := range r.Matches {
		path := Pointer("/matches", strconv.Itoa(i))
		if match.InputIndex >= len(request.Values) || seen[match.InputIndex] {
			return invalid("invalidInputIndex", Pointer(path, "inputIndex"))
		}
		seen[match.InputIndex] = true
		if match.Item != nil {
			if err := validateResourceItem(*match.Item, Pointer(path, "item")); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateResourceItem(item ResourceItem, path string) error {
	if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.Title) == "" {
		return invalid("invalidResourceItem", path)
	}
	if !item.Selectable && strings.TrimSpace(item.Reason) == "" {
		return invalid("unselectableReasonRequired", Pointer(path, "reason"))
	}
	return nil
}

func (r PrepareCopyRequest) Validate() error { return validateTyped("prepareCopyRequest", r) }
func (r PrepareCopyResponse) Validate() error {
	if err := validateTyped("prepareCopyResponse", r); err != nil {
		return err
	}
	return r.validatePlan()
}
func (r ValidateCopySettingsRequest) Validate() error {
	return validateTyped("validateCopySettingsRequest", r)
}
