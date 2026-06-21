package settings

import "testing"

func TestAdoptPromptThreshold(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		s    *EntireSettings
		want int
	}{
		{"nil settings", nil, DefaultAdoptPromptAfterCommits},
		{"unset (zero)", &EntireSettings{}, DefaultAdoptPromptAfterCommits},
		{"explicit positive", &EntireSettings{AdoptPromptAfterCommits: 10}, 10},
		{"explicit one", &EntireSettings{AdoptPromptAfterCommits: 1}, 1},
		{"disabled (negative)", &EntireSettings{AdoptPromptAfterCommits: -1}, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.s.AdoptPromptThreshold(); got != tc.want {
				t.Fatalf("AdoptPromptThreshold() = %d, want %d", got, tc.want)
			}
		})
	}
}
