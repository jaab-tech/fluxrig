package gears

import (
	"strings"
	"testing"
)

// The lean build's contract, which the tech-stack page publishes: a scenario
// naming a gear the binary does not carry fails loudly at creation rather than
// degrading into a no-op. Compiled both ways, this asserts the half that
// applies to the binary running it.
func TestFactoryRejectsUnknownGearType(t *testing.T) {
	f := NewFactory()
	_, err := f.Create("definitely_not_a_gear")
	if err == nil {
		t.Fatal("an unknown gear type was accepted")
	}
	if !strings.Contains(err.Error(), "unknown gear type") {
		t.Fatalf("error does not name the problem: %v", err)
	}
}

// With the nobento tag, "bento" is exactly such a type. Without it, it is
// registered. Both are the documented behaviour, so both are asserted.
func TestBentoRegistrationFollowsBuildTag(t *testing.T) {
	f := NewFactory()
	_, err := f.Create("bento")
	if bentoCompiledIn && err != nil {
		t.Fatalf("the default build must carry the bento gear: %v", err)
	}
	if !bentoCompiledIn {
		if err == nil {
			t.Fatal("a lean build accepted the bento gear")
		}
		if !strings.Contains(err.Error(), "unknown gear type: bento") {
			t.Fatalf("a lean build must say which type is missing, got: %v", err)
		}
	}
}
