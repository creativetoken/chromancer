package rodbackend

import "testing"

func TestChromeMajor(t *testing.T) {
	cases := map[string]int{
		"HeadlessChrome/121.0.6167.85": 121,
		"Chrome/142.0.1.2":             142,
		"121.0.6167.85":                121,
		"":                             0,
		"garbage":                      0,
		"99":                           99,
	}
	for in, want := range cases {
		if got := chromeMajor(in); got != want {
			t.Errorf("chromeMajor(%q) = %d, want %d", in, got, want)
		}
	}
}
