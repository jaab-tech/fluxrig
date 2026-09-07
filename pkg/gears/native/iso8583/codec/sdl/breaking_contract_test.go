package sdl

import (
	"strings"
	"testing"
)

// v0.10.0 breaks on two things, and the changelog tells an operator both are
// refused "at load: at boot, with a message naming the spec, never per
// transaction". These assert that the refusal happens and that the message is
// the one promised, because a break whose failure mode is undocumented is worse
// than no break.
func TestSpecIdentityIsRequired(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "no id",
			yaml: "spec:\n  name: x\n  version: \"1.0.0\"\n  wire:\n    source: \"moov:spec87ascii\"\n",
			want: "spec.id is required",
		},
		{
			name: "no version",
			yaml: "spec:\n  id: acme\n  name: x\n  wire:\n    source: \"moov:spec87ascii\"\n",
			want: `spec "acme": spec.version is required`,
		},
		{
			name: "version is not semver",
			yaml: "spec:\n  id: acme\n  name: x\n  version: \"1.0\"\n  wire:\n    source: \"moov:spec87ascii\"\n",
			want: `spec "acme": spec.version "1.0" is not a semantic version`,
		},
		{
			name: "legacy vocabulary has no wire layer at all",
			yaml: "meta:\n  name: legacy\nfields:\n  2:\n    type: llvar_n\n",
			want: "no wire source",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := LoadSpecContent([]byte(tc.yaml), "")
			if err == nil {
				t.Fatal("the spec loaded")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("message does not carry %q: %v", tc.want, err)
			}
		})
	}
}
