package main

import "testing"

func TestEmbedDestination(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		current  string
		explicit map[string]bool
		dev      bool
		watch    bool
		want     string
	}{
		{name: "default production build", explicit: map[string]bool{}, want: defaultEmbedDir},
		{name: "verbose production build", explicit: map[string]bool{"v": true}, want: defaultEmbedDir},
		{name: "custom output", explicit: map[string]bool{"out": true}},
		{name: "custom source", explicit: map[string]bool{"src": true}},
		{name: "custom base", explicit: map[string]bool{"base": true}},
		{name: "development", explicit: map[string]bool{}, dev: true},
		{name: "watch", explicit: map[string]bool{}, watch: true},
		{
			name:     "explicit mirror",
			current:  "tmp/embed",
			explicit: map[string]bool{"embed": true},
			want:     "tmp/embed",
		},
		{name: "explicitly disabled mirror", explicit: map[string]bool{"embed": true}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := embedDestination(tt.current, tt.explicit, tt.dev, tt.watch); got != tt.want {
				t.Fatalf("embedDestination() = %q, want %q", got, tt.want)
			}
		})
	}
}
