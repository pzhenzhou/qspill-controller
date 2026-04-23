package config_test

import (
	"sort"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/pzhenzhou/qspill-controller/pkg/config"
)

// loadOrFail returns a registry from a testdata fixture or fails the test;
// the registry tests don't care about LoadFromBytes failure paths (covered
// in config_test.go) — they need a known-good registry to introspect.
func loadOrFail(t *testing.T, fixture string) *config.PolicyRegistry {
	t.Helper()
	clearConfigEnv(t)
	data := readFixture(t, fixture)
	reg, err := config.LoadFromBytes(data)
	if err != nil {
		t.Fatalf("LoadFromBytes(%s) failed: %v", fixture, err)
	}
	return reg
}

func TestPolicyByNameAndQueue(t *testing.T) {
	reg := loadOrFail(t, "valid_baseline.yaml")

	if p, ok := reg.PolicyByName("biz-a"); !ok || p.QueueName != "biz-a" {
		t.Errorf("PolicyByName(biz-a) miss or wrong: %+v ok=%v", p, ok)
	}
	if p, ok := reg.PolicyByQueue("biz-b"); !ok || p.Name != "biz-b" {
		t.Errorf("PolicyByQueue(biz-b) miss or wrong: %+v ok=%v", p, ok)
	}
	if _, ok := reg.PolicyByName("ghost"); ok {
		t.Error("PolicyByName(ghost) should miss")
	}
	if _, ok := reg.PolicyByQueue("ghost"); ok {
		t.Error("PolicyByQueue(ghost) should miss")
	}
}

// TestPoliciesForNodeGroupSingle covers the simplest reverse-index case: one
// policy with a distinct dedicated and overflow group; each value resolves to
// exactly that policy.
func TestPoliciesForNodeGroupSingle(t *testing.T) {
	reg := loadOrFail(t, "valid_minimal.yaml")
	assertNodeGroup(t, reg, "ded", []string{"only"})
	assertNodeGroup(t, reg, "ovr", []string{"only"})
	assertNodeGroup(t, reg, "ghost", nil)
}

// TestPoliciesForNodeGroupMulti covers the baseline two-policy case where the
// dedicated and overflow groups are distinct across all four values.
func TestPoliciesForNodeGroupMulti(t *testing.T) {
	reg := loadOrFail(t, "valid_baseline.yaml")
	assertNodeGroup(t, reg, "ng2", []string{"biz-a"})
	assertNodeGroup(t, reg, "ng1", []string{"biz-a"})
	assertNodeGroup(t, reg, "ng4", []string{"biz-b"})
	assertNodeGroup(t, reg, "ng3", []string{"biz-b"})
}

// TestPoliciesForNodeGroupSharedOverflow covers the canonical multi-tenant
// case: every policy has its own dedicated nodegroup but they all share one
// overflow pool. The reverse index must list every policy under the shared
// pool's value, in stable order.
func TestPoliciesForNodeGroupSharedOverflow(t *testing.T) {
	reg := loadOrFail(t, "registry_shared_overflow.yaml")
	assertNodeGroup(t, reg, "ng2", []string{"biz-a"})
	assertNodeGroup(t, reg, "ng4", []string{"biz-b"})
	assertNodeGroup(t, reg, "ng6", []string{"biz-c"})
	assertNodeGroup(t, reg, "pool", []string{"biz-a", "biz-b", "biz-c"})
}

// TestPoliciesForNodeGroupReturnsCopy guarantees the caller cannot mutate the
// registry's internal slice via the returned reference; mutations to the copy
// must not bleed back into subsequent calls.
func TestPoliciesForNodeGroupReturnsCopy(t *testing.T) {
	reg := loadOrFail(t, "registry_shared_overflow.yaml")
	first := reg.PoliciesForNodeGroup("pool")
	if len(first) == 0 {
		t.Fatal("expected non-empty result")
	}
	first[0] = "mutated"
	second := reg.PoliciesForNodeGroup("pool")
	if second[0] == "mutated" {
		t.Errorf("registry leaked internal slice; second call returned %v", second)
	}
}

// TestPoliciesReturnsCopy guarantees the same property for the policies
// slice. Callers may freely sort or trim the returned slice without affecting
// the registry's canonical ordering.
func TestPoliciesReturnsCopy(t *testing.T) {
	reg := loadOrFail(t, "valid_baseline.yaml")
	first := reg.Policies()
	if len(first) < 2 {
		t.Fatal("expected at least 2 policies")
	}
	first[0] = nil
	second := reg.Policies()
	if second[0] == nil {
		t.Error("registry leaked internal slice; second call has nil head")
	}
}

func TestRegistryStoreSeededWithEmpty(t *testing.T) {
	store := config.NewRegistryStore()
	got := store.Get()
	if got == nil {
		t.Fatal("seeded store returned nil")
	}
	if policies := got.Policies(); len(policies) != 0 {
		t.Errorf("seeded store has %d policies, want 0", len(policies))
	}
}

func TestRegistryStoreSetNilIgnored(t *testing.T) {
	store := config.NewRegistryStore()
	reg := loadOrFail(t, "valid_baseline.yaml")
	store.Set(reg)
	store.Set(nil)
	if got := store.Get(); got != reg {
		t.Errorf("Set(nil) overwrote prior registry; got %p want %p", got, reg)
	}
}

// TestRegistryStoreConcurrentSwap exercises the atomic.Pointer contract under
// the race detector: many readers and writers contend for the same store and
// no reader observes a nil registry, no writer corrupts a reader's view.
func TestRegistryStoreConcurrentSwap(t *testing.T) {
	store := config.NewRegistryStore()
	regA := loadOrFail(t, "valid_baseline.yaml")
	regB := loadOrFail(t, "valid_minimal.yaml")
	store.Set(regA)

	const (
		readers     = 16
		writers     = 4
		readsPerG   = 5000
		writesPerG  = 500
		expectedReg = "biz-a or only"
	)

	var (
		wg         sync.WaitGroup
		readsDone  int64
		writesDone int64
	)

	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < readsPerG; j++ {
				r := store.Get()
				if r == nil {
					t.Error("Get returned nil during concurrent swap")
					return
				}
				policies := r.Policies()
				if len(policies) == 0 || policies[0] == nil {
					t.Errorf("registry observed in inconsistent state: %v", policies)
					return
				}
				name := policies[0].Name
				if name != "biz-a" && name != "only" {
					t.Errorf("unexpected first-policy name %q (want %s)", name, expectedReg)
					return
				}
				atomic.AddInt64(&readsDone, 1)
			}
		}()
	}

	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < writesPerG; j++ {
				if (id+j)%2 == 0 {
					store.Set(regA)
				} else {
					store.Set(regB)
				}
				atomic.AddInt64(&writesDone, 1)
			}
		}(i)
	}

	wg.Wait()

	if got := atomic.LoadInt64(&readsDone); got != int64(readers*readsPerG) {
		t.Errorf("not all reads completed: got %d", got)
	}
	if got := atomic.LoadInt64(&writesDone); got != int64(writers*writesPerG) {
		t.Errorf("not all writes completed: got %d", got)
	}
}

// assertNodeGroup checks the reverse-index lookup against an expected sorted
// list of policy names. The helper sorts both sides because the registry's
// insertion order matches YAML order, and tests should not entangle iteration
// order with that detail.
func assertNodeGroup(t *testing.T, reg *config.PolicyRegistry, value string, want []string) {
	t.Helper()
	got := reg.PoliciesForNodeGroup(value)
	sort.Strings(got)
	wantCopy := append([]string(nil), want...)
	sort.Strings(wantCopy)
	if !equalStringSlice(got, wantCopy) {
		t.Errorf("PoliciesForNodeGroup(%q): got %v want %v", value, got, wantCopy)
	}
}

func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
