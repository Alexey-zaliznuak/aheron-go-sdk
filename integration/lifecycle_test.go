package integration

import (
	"errors"
	"reflect"
	"testing"
)

const (
	lifecycleTestProject     = "10000000-0000-0000-0000-000000000001"
	lifecycleTestIntegration = "20000000-0000-0000-0000-000000000001"
	lifecycleTestFirst       = "30000000-0000-0000-0000-000000000001"
	lifecycleTestNext        = "30000000-0000-0000-0000-000000000002"
)

func lifecycleTestRequest(n int64, action, installation string) LifecycleRequest {
	return LifecycleRequest{Protocol: LifecycleProtocol, EventID: "40000000-0000-0000-0000-00000000000" + string(rune('0'+n)), ProjectID: lifecycleTestProject, IntegrationID: lifecycleTestIntegration, InstallationID: installation, Sequence: n, Action: action}
}

func TestLifecycleReorderAndTombstone(t *testing.T) {
	events := []LifecycleRequest{
		lifecycleTestRequest(1, LifecycleInstall, lifecycleTestFirst),
		lifecycleTestRequest(2, LifecycleUninstall, lifecycleTestFirst),
		lifecycleTestRequest(3, LifecycleInstall, lifecycleTestNext),
		lifecycleTestRequest(4, LifecycleUninstall, lifecycleTestNext),
	}
	var permutations func([]int, []int)
	permutations = func(order, remaining []int) {
		if len(remaining) != 0 {
			for i, n := range remaining {
				permutations(append(append([]int{}, order...), n), append(append([]int{}, remaining[:i]...), remaining[i+1:]...))
			}
			return
		}
		var state LifecycleState
		for _, index := range order {
			request := events[index]
			decision, err := DecideLifecycle(state, request)
			if err != nil {
				t.Fatal(order, err)
			}
			if decision.Receipt.ValidateFor(request) != nil {
				t.Fatal("invalid generated receipt")
			}
			if decision.Receipt.Outcome == LifecycleApplied {
				state = decision.State
			}
			replay, err := DecideLifecycle(state, request)
			if err != nil || replay.Receipt.Outcome == LifecycleApplied || replay.State != state {
				t.Fatalf("replay mutated %v: %+v %v", order, replay, err)
			}
		}
		if state.Sequence != 4 || state.InstallationID != lifecycleTestNext || state.Action != LifecycleUninstall {
			t.Fatalf("order %v did not converge: %+v", order, state)
		}
	}
	permutations(nil, []int{0, 1, 2, 3})
	newInstall, _ := DecideLifecycle(LifecycleState{}, events[2])
	late, err := DecideLifecycle(newInstall.State, events[1])
	if err != nil || late.Receipt.Outcome != LifecycleSuperseded || late.State != newInstall.State {
		t.Fatalf("late uninstall changed replacement: %+v %v", late, err)
	}
	closed, _ := DecideLifecycle(LifecycleState{}, events[1])
	reopen := lifecycleTestRequest(5, LifecycleInstall, lifecycleTestFirst)
	if _, err := DecideLifecycle(closed.State, reopen); !errors.Is(err, ErrLifecycleConflict) {
		t.Fatalf("closed lifetime reopened: %v", err)
	}
}

func TestLifecycleConflictsAndBoundaries(t *testing.T) {
	req := lifecycleTestRequest(1, LifecycleInstall, lifecycleTestFirst)
	first, err := DecideLifecycle(LifecycleState{}, req)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*LifecycleRequest){
		func(r *LifecycleRequest) { r.InstallationID = lifecycleTestNext },
		func(r *LifecycleRequest) { r.ProjectID = lifecycleTestIntegration },
		func(r *LifecycleRequest) { r.IntegrationID = lifecycleTestProject },
		func(r *LifecycleRequest) { r.Sequence = 2 },
	} {
		changed := req
		mutate(&changed)
		if _, err := DecideLifecycle(first.State, changed); !errors.Is(err, ErrLifecycleConflict) {
			t.Fatalf("accepted conflicting message: %v", err)
		}
	}
	for _, corrupt := range []LifecycleState{{Sequence: 1}, {ProjectID: req.ProjectID}, {Sequence: -1}} {
		if _, err := DecideLifecycle(corrupt, req); !errors.Is(err, ErrLifecycleState) {
			t.Fatalf("corrupt watermark treated as absent: %v", err)
		}
	}
	for _, mutate := range []func(*LifecycleRequest){
		func(r *LifecycleRequest) { r.Protocol = "other" },
		func(r *LifecycleRequest) { r.Sequence = 0 },
		func(r *LifecycleRequest) { r.Action = "pause" },
		func(r *LifecycleRequest) { r.EventID = "00000000-0000-0000-0000-000000000000" },
		func(r *LifecycleRequest) { r.ProjectID = "invalid" },
	} {
		changed := req
		mutate(&changed)
		if err := changed.Validate(); !errors.Is(err, ErrLifecycleInvalid) {
			t.Fatalf("accepted invalid message: %v", err)
		}
	}
	for _, mutate := range []func(*LifecycleReceipt){
		func(r *LifecycleReceipt) { r.EventID = lifecycleTestNext },
		func(r *LifecycleReceipt) {
			r.RequestDigest = "0000000000000000000000000000000000000000000000000000000000000000"
		},
		func(r *LifecycleReceipt) { r.Outcome = LifecycleSuperseded },
		func(r *LifecycleReceipt) { r.ObservedSequence++ },
	} {
		changed := first.Receipt
		mutate(&changed)
		if err := changed.ValidateFor(req); !errors.Is(err, ErrLifecycleInvalid) {
			t.Fatalf("accepted mismatched receipt: %+v %v", changed, err)
		}
	}
	duplicate, err := DecideLifecycle(first.State, req)
	if err != nil || !reflect.DeepEqual(duplicate.State, first.State) || duplicate.Receipt.Outcome != LifecycleDuplicate {
		t.Fatal(duplicate, err)
	}
}
