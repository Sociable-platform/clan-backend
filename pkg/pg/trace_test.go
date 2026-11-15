package pg

import "testing"

func TestOperation(t *testing.T) {
	cases := map[string]string{
		"select id from clans where id = $1":        "SELECT",
		"\n\t  insert into outbox (id) values ($1)": "INSERT",
		"begin": "BEGIN",
		"":      "QUERY",
	}
	for sql, want := range cases {
		if got := operation(sql); got != want {
			t.Errorf("operation(%q) = %q, want %q", sql, got, want)
		}
	}
}
