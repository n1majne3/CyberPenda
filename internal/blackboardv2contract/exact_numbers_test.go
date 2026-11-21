package blackboardv2contract_test

import "testing"

func TestHarnessPreservesExactIntegerNumbers(t *testing.T) {
	harness := mustHarness(t)
	for _, schema := range []string{"revision", "version"} {
		t.Run(schema, func(t *testing.T) {
			for _, test := range []struct {
				raw  string
				want bool
			}{
				{raw: "1", want: true},
				{raw: "1.0", want: true},
				{raw: "1e0", want: true},
				{raw: "1.000e3", want: true},
				{raw: "9007199254740991", want: true},
				{raw: "9007199254740991.0", want: true},
				{raw: "0", want: schema == "revision"},
				{raw: "-1"},
				{raw: "1.5"},
				{raw: "1.0000000000000001"},
				{raw: "9007199254740991.1"},
				{raw: "9007199254740992"},
				{raw: "1e-1000"},
				{raw: "1e309"},
				{raw: `"1"`},
				{raw: "null"},
				{raw: ""},
			} {
				t.Run(test.raw, func(t *testing.T) {
					err := harness.Validate(schema, []byte(test.raw))
					if (err == nil) != test.want {
						t.Fatalf("Validate(%s, %q) = %v; want valid %t", schema, test.raw, err, test.want)
					}
				})
			}
		})
	}
}
