package plots

import (
	"strings"
	"testing"
)

func TestExtractPlot(t *testing.T) {
	tests := []struct {
		name      string
		wikitext  string
		want      string
		wantWords int
	}{
		{
			name:      "takes the plot section and stops at the next one",
			wikitext:  "{{Infobox film|name=X}}\nLead text.\n== Plot ==\nA thief plans a heist.\n=== Ending ===\nIt fails.\n== Cast ==\n* Someone",
			want:      "A thief plans a heist. It fails.",
			wantWords: 7,
		},
		{
			name:      "matches the heading case-insensitively and without spaces",
			wikitext:  "==Plot summary==\nA story.",
			want:      "A story.",
			wantWords: 2,
		},
		{
			name:      "reads a premise for a show",
			wikitext:  "== Production ==\nShot in Ohio.\n== Premise ==\nA family runs a diner.",
			want:      "A family runs a diner.",
			wantWords: 5,
		},
		{
			name:     "ignores a plot heading below level 2",
			wikitext: "== Story arcs ==\n=== Plot ===\nNot this.\n",
			want:     "",
		},
		{
			name:     "has no plot section",
			wikitext: "Lead.\n== Reception ==\nGood.",
			want:     "",
		},
		{
			name: "strips references, templates, files, links and markup",
			wikitext: "== Plot ==\n<!-- note -->[[File:A.jpg|thumb|A [[still]] from it]]" +
				"'''Ann''' meets [[Bob Smith|Bob]] in [[Paris]].<ref name=\"a\">{{cite web|url=x}}</ref>" +
				"{{efn|a {{nested}} note}} They talk.<ref name=b/> See [https://example.org the site]&nbsp;now.\n" +
				"{| class=wikitable\n| a || b\n|}\n<br />End.",
			want:      "Ann meets Bob in Paris. They talk. See the site now. End.",
			wantWords: 12,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, words := ExtractPlot(tt.wikitext)
			if got != tt.want {
				t.Errorf("ExtractPlot() text = %q, want %q", got, tt.want)
			}
			if words != tt.wantWords {
				t.Errorf("ExtractPlot() words = %d, want %d", words, tt.wantWords)
			}
		})
	}
}

func TestEmbedInput(t *testing.T) {
	long := strings.TrimSpace(strings.Repeat("word ", MaxInputWords+50))
	tests := []struct {
		name  string
		plot  string
		words int
	}{
		{"keeps a short plot whole", "a short plot", 3},
		{"cuts a long plot", long, MaxInputWords},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := len(strings.Fields(EmbedInput(tt.plot))); got != tt.words {
				t.Errorf("EmbedInput kept %d words, want %d", got, tt.words)
			}
		})
	}
}
