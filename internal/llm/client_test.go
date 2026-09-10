package llm

import "testing"

func TestExtractJSON(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "bare object",
			in:   `{"name":"x","kind":"cron"}`,
			want: `{"name":"x","kind":"cron"}`,
		},
		{
			name: "fenced code block",
			in:   "```json\n{\"name\":\"x\"}\n```",
			want: `{"name":"x"}`,
		},
		{
			name: "fenced without language",
			in:   "```\n{\"name\":\"x\"}\n```",
			want: `{"name":"x"}`,
		},
		{
			name: "prose around object",
			in:   "好的，规则如下：{\"name\":\"x\",\"kind\":\"cron\"} 请确认。",
			want: `{"name":"x","kind":"cron"}`,
		},
		{
			name: "array",
			in:   `[{"a":1},{"a":2}]`,
			want: `[{"a":1},{"a":2}]`,
		},
		{
			name: "no json",
			in:   "没有任何内容",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractJSON(tc.in); got != tc.want {
				t.Fatalf("extractJSON(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestDisabledClient(t *testing.T) {
	c := New("", "", "")
	if c.Enabled() {
		t.Fatal("client with no config should be disabled")
	}
	if _, err := c.Complete(nil, "", ""); err != errDisabled {
		t.Fatalf("Complete on disabled client = %v, want errDisabled", err)
	}
	var v any
	if err := c.CompleteJSON(nil, "", "", &v); err != errDisabled {
		t.Fatalf("CompleteJSON on disabled client = %v, want errDisabled", err)
	}
}
