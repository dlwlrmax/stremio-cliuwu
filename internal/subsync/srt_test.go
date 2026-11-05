package subsync

import "testing"

func TestToSRT(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{
			name: "subrip passes through unchanged",
			in:   "1\n00:00:01,000 --> 00:00:02,000\nhi\n",
			want: "1\n00:00:01,000 --> 00:00:02,000\nhi\n",
		},
		{name: "empty", in: "", want: ""},
		{
			name: "bom stripped and missing hour added",
			in:   "\xef\xbb\xbfWEBVTT\n\n00:01.000 --> 00:02.500\nhello\n",
			want: "1\n00:00:01,000 --> 00:00:02,500\nhello\n\n",
		},
		{
			name: "cue identifiers dropped and cues renumbered",
			in:   "WEBVTT\n\nintro\n00:00:01.000 --> 00:00:02.000\na\n\nsecond\n00:00:03.000 --> 00:00:04.000\nb\n",
			want: "1\n00:00:01,000 --> 00:00:02,000\na\n\n2\n00:00:03,000 --> 00:00:04,000\nb\n\n",
		},
		{
			name: "cue settings dropped",
			in:   "WEBVTT\n\n00:00:01.000 --> 00:00:02.000 align:start position:0%\nhi\n",
			want: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n",
		},
		{
			name: "header title, NOTE and STYLE blocks skipped",
			in: "WEBVTT - some title\n\nNOTE this is\na two line note\n\n" +
				"STYLE\n::cue { color: red }\n\n00:00:01.000 --> 00:00:02.000\nhi\n",
			want: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n",
		},
		{
			name: "inline tags stripped, i and b kept",
			in:   "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\n<c.yellow>x</c> <v Bob>hi <i>y</i> <b>z</b>\n",
			want: "1\n00:00:01,000 --> 00:00:02,000\nx hi <i>y</i> <b>z</b>\n\n",
		},
		{
			name: "crlf line endings",
			in:   "WEBVTT\r\n\r\n00:00:01.000 --> 00:00:02.000\r\nhi\r\n",
			want: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n",
		},
		{
			name: "multi-line cue keeps its breaks",
			in:   "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nline one\nline two\n",
			want: "1\n00:00:01,000 --> 00:00:02,000\nline one\nline two\n\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := string(ToSRT([]byte(c.in))); got != c.want {
				t.Errorf("ToSRT(%q)\n got: %q\nwant: %q", c.in, got, c.want)
			}
		})
	}
}
